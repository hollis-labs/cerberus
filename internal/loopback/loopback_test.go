package loopback

import (
	"net"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
)

func TestCheckListen(t *testing.T) {
	for _, tc := range []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:4783", true},
		{"127.0.0.2:4783", true},
		{"localhost:4783", true},
		{"LOCALHOST:4783", true},
		{"[::1]:4785", true},
		{"0.0.0.0:4783", false},
		{"[::]:4783", false},
		{":4783", false},
		{"192.168.1.10:4783", false},
		{"10.0.0.5:4785", false},
		{"[fe80::1]:4785", false},
		{"not-an-address", false},
		// Names other than localhost are refused, even ones that resolve to
		// loopback, so nothing is resolved twice or trusted as a Host.
		{"myhost.local:4783", false},
		{"localhost.localdomain:4783", false},
		{"ip6-localhost:4783", false},
	} {
		err := CheckListen("cerberus web", tc.addr)
		if (err == nil) != tc.ok {
			t.Errorf("CheckListen(%q) err = %v, want ok=%v", tc.addr, err, tc.ok)
		}
	}
}

func TestCheckListenRefusalNamesTheReason(t *testing.T) {
	err := CheckListen("cerberus mcp-http", "0.0.0.0:4785")
	if err == nil {
		t.Fatal("expected refusal")
	}
	for _, want := range []string{"cerberus mcp-http", "0.0.0.0:4785", "no authentication", "localhost, 127.0.0.1 or [::1]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q missing %q", err.Error(), want)
		}
	}
}

// main prints every CLI error through redact.Text, and the web UI writes JSON
// through redact.Marshal. A guard whose explanation gets eaten on the way out
// is worse than no explanation.
func TestRefusalMessagesSurviveRedaction(t *testing.T) {
	msgs := []string{HostRejected, OriginRejected}
	for _, addr := range []string{"0.0.0.0:4783", ":4785", "not-an-address", "myhost.local:4783"} {
		msgs = append(msgs, CheckListen("cerberus web", addr).Error())
	}
	for _, msg := range msgs {
		if got := redact.Text(msg); got != msg {
			t.Errorf("redact.Text changed refusal:\n got %q\nwant %q", got, msg)
		}
		data, err := redact.Marshal(map[string]any{"success": false, "error": msg})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), strings.ReplaceAll(msg, `"`, `\"`)) {
			t.Errorf("redact.Marshal changed refusal: %s", data)
		}
	}
}

func TestCheckListenRefusesHostnamesNamingTheAcceptedForms(t *testing.T) {
	err := CheckListen("cerberus web", "myhost.local:4783")
	if err == nil {
		t.Fatal("expected refusal")
	}
	for _, want := range []string{"localhost", "127.0.0.1", "[::1]", "not a hostname"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q missing %q", err.Error(), want)
		}
	}
}

func TestGuardHostAllowed(t *testing.T) {
	g := NewGuard("127.0.0.1", "4783")
	for _, tc := range []struct {
		host string
		ok   bool
	}{
		{"127.0.0.1:4783", true},
		{"localhost:4783", true},
		{"LocalHost:4783", true},
		{"[::1]:4783", true},
		{"localhost", true},
		// Any port: an ssh -L onto another local port, or the Vite dev proxy.
		{"localhost:9000", true},
		{"evil.example:4783", false},
		{"evil.example", false},
		{"127.0.0.1.evil.example:4783", false},
		{"localhost.evil.example:4783", false},
		{"", false},
	} {
		if got := g.HostAllowed(tc.host); got != tc.ok {
			t.Errorf("HostAllowed(%q) = %v, want %v", tc.host, got, tc.ok)
		}
	}
}

func TestGuardAcceptsConfiguredListenHost(t *testing.T) {
	g := NewGuard("127.0.0.2", "4783")
	if !g.HostAllowed("127.0.0.2:4783") {
		t.Fatal("configured listen host should be allowed")
	}
	if !g.OriginAllowed("http://127.0.0.2:4783") {
		t.Fatal("configured listen host origin should be allowed")
	}
}

func TestGuardOriginAllowed(t *testing.T) {
	g := NewGuard("127.0.0.1", "4783", "https://inspector.example")
	for _, tc := range []struct {
		origin string
		ok     bool
	}{
		{"http://127.0.0.1:4783", true},
		{"http://localhost:4783", true},
		{"http://[::1]:4783", true},
		{"HTTP://LOCALHOST:4783", true},
		{"https://inspector.example", true},
		{"http://127.0.0.1:1234", false},
		{"http://localhost", false},
		{"http://evil.example:4783", false},
		{"null", false},
		{"", false},
	} {
		if got := g.OriginAllowed(tc.origin); got != tc.ok {
			t.Errorf("OriginAllowed(%q) = %v, want %v", tc.origin, got, tc.ok)
		}
	}
}

func TestNewGuardForAddrTakesPortFromListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck
	g, err := NewGuardForAddr("127.0.0.1:0", ln.Addr())
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	if !g.OriginAllowed("http://127.0.0.1:" + port) {
		t.Fatalf("origin on bound port %s should be allowed", port)
	}
}
