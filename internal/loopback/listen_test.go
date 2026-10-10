package loopback

import (
	"errors"
	"github.com/hollis-labs/cerberus/internal/redact"
	"net"
	"strings"
	"testing"
)

// Both loopback families on one port.
func TestListenBothBindsBothFamilies(t *testing.T) {
	lns, err := ListenBoth("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, ln := range lns {
			_ = ln.Close()
		}
	}()
	if len(lns) == 1 {
		t.Skip("no IPv6 loopback on this machine")
	}
	_, p4, _ := net.SplitHostPort(lns[0].Addr().String())
	h6, p6, _ := net.SplitHostPort(lns[1].Addr().String())
	if p4 != p6 || h6 != "::1" {
		t.Fatalf("bound %s and %s", lns[0].Addr(), lns[1].Addr())
	}
}

// Another process holding [::1] on the port is refused, not started beside.
func TestListenBothRefusesASquatter(t *testing.T) {
	squatter, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback on this machine")
	}
	defer func() {
		if closeErr := squatter.Close(); closeErr != nil {
			t.Errorf("close squatter listener: %v", closeErr)
		}
	}()
	_, port, _ := net.SplitHostPort(squatter.Addr().String())
	lns, err := ListenBoth("127.0.0.1:" + port)
	var squatted *SquattedError
	if !errors.As(err, &squatted) || len(lns) != 0 {
		for _, ln := range lns {
			_ = ln.Close()
		}
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "[::1]:"+port) || !strings.Contains(err.Error(), "lsof -nP -iTCP:"+port) {
		t.Fatalf("err = %v", err)
	}
	// The refusal names what a squatter would get, for a console and for
	// mcp-http, and survives the redaction every surface's errors get.
	if !strings.Contains(err.Error(), "the token an MCP client sends") {
		t.Fatalf("err = %v", err)
	}
	if got := redact.Text(err.Error()); got != err.Error() {
		t.Fatalf("redaction rewrote the refusal: %q", got)
	}
	// The 127.0.0.1 half was released, not leaked.
	if ln, err := net.Listen("tcp", "127.0.0.1:"+port); err != nil {
		t.Fatalf("127.0.0.1:%s still held: %v", port, err)
	} else {
		_ = ln.Close()
	}
}
