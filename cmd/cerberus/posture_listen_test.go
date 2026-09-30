package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/loopback"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

func withCurrentPosture(t *testing.T, s policy.PostureSummary) {
	t.Helper()
	old := currentPosture
	currentPosture = func() policy.PostureSummary { return s }
	t.Cleanup(func() { currentPosture = old })
}

func withMCPHTTPFlags(t *testing.T, listen string, insecure bool, hosts ...string) {
	t.Helper()
	oldListen, oldInsecure, oldHosts := mcpHTTPListen, mcpHTTPInsecure, mcpHTTPHosts
	mcpHTTPListen, mcpHTTPInsecure, mcpHTTPHosts = listen, insecure, hosts
	t.Cleanup(func() { mcpHTTPListen, mcpHTTPInsecure, mcpHTTPHosts = oldListen, oldInsecure, oldHosts })
}

// mcp-http leaves loopback with auth and TLS in any posture, or with
// --insecure-listen (no auth) under a global permissive posture only; a
// scoped rule never reaches this switch.
func TestMCPHTTPInsecureListenNeedsThePermissivePosture(t *testing.T) {
	secure := policy.PostureSummary{Global: policy.PostureSecure}
	scoped := policy.File{PostureRules: []policy.PostureRule{{Match: policy.TargetMatch{Env: "dev"}, Posture: policy.PosturePermissive}}}.PostureSummary("h")
	global := policy.File{Posture: policy.PosturePermissive}.PostureSummary("h")
	for _, c := range []struct {
		name     string
		posture  policy.PostureSummary
		listen   string
		insecure bool
		hosts    []string
		want     string
		auth     bool
		tlsOn    bool
	}{
		{"loopback, secure", secure, "127.0.0.1:4785", false, nil, "", false, false},
		{"off loopback without the flag is refused even when permissive", global, "0.0.0.0:4785", false, nil, "needs auth and TLS", false, false},
		{"the flag under secure is refused", secure, "0.0.0.0:4785", true, nil, "only under the permissive posture", false, false},
		{"the flag under a scoped rule is refused", scoped, "0.0.0.0:4785", true, nil, "only under the permissive posture", false, false},
		{"the flag under global permissive", global, "0.0.0.0:4785", true, []string{"box.lan"}, "", false, false},
		{"--allow-host on loopback is refused", global, "127.0.0.1:4785", false, []string{"box.lan"}, "--allow-host is for a listener off loopback", false, false},
		{"off loopback with auth and TLS, secure", secure, "0.0.0.0:4785", false, []string{"box.lan"}, "", true, true},
		{"off loopback with auth but no TLS", secure, "0.0.0.0:4785", false, nil, "needs TLS", true, false},
		{"off loopback with TLS but no auth", secure, "0.0.0.0:4785", false, nil, "needs auth and TLS", false, true},
		{"the insecure flag with auth configured", global, "0.0.0.0:4785", true, nil, "auth is configured", true, true},
		{"loopback with auth, no TLS", secure, "127.0.0.1:4785", false, nil, "", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			withCurrentPosture(t, c.posture)
			withMCPHTTPFlags(t, c.listen, c.insecure, c.hosts...)
			err := checkMCPHTTPListen(c.auth, c.tlsOn)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("err = %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if err != nil && redact.Text(err.Error()) != err.Error() {
				t.Fatalf("redaction rewrote the refusal: %q", redact.Text(err.Error()))
			}
		})
	}
}

// An insecure listener answers the names the operator allowed, and still
// refuses any other Host, so a rebinding page is turned away.
func TestInsecureListenerGuardAllowsOnlyTheNamedHosts(t *testing.T) {
	g := loopback.NewGuard("0.0.0.0", "4785")
	g.AllowHosts("box.lan", "192.168.1.20")
	h := mcpHTTPHandler(buildCerberusMCPServer(nil), "/mcp", g, nil)
	for host, want := range map[string]int{"box.lan:4785": http.StatusOK, "192.168.1.20:4785": http.StatusOK, "evil.example:4785": http.StatusForbidden} {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %s: %d, want %d", host, rec.Code, want)
		}
	}
}

// The web console stays loopback-only in every posture until it serves TLS.
func TestWebConsoleStaysLoopbackUnderPermissive(t *testing.T) {
	withCurrentPosture(t, policy.File{Posture: policy.PosturePermissive}.PostureSummary("h"))
	old := webListenAddr
	webListenAddr = "0.0.0.0:4783"
	t.Cleanup(func() { webListenAddr = old })
	webCmd.SetContext(context.Background())
	err := webCmd.RunE(webCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not a loopback address") {
		t.Fatalf("web on 0.0.0.0 under permissive: %v", err)
	}
}
