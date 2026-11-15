package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	cmcp "github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// enforceAll enforces policy on every request.
type enforceAll struct{}

func (enforceAll) Enforced(policy.Request) bool { return true }

// clientPolicy denies the client "bad-client" on web, allows the rest, and trips the circuit
// breaker after two denials.
func clientPolicy() gatetest.Options {
	file := policy.File{Version: 1, CircuitBreaker: &policy.CircuitBreaker{Denials: 2, Window: time.Minute},
		Targets: []policy.TargetBlock{{Match: policy.TargetMatch{ID: "web"}, Rules: []policy.Rule{
			{ID: "block-bad-client", Principal: &policy.PrincipalMatch{Client: "bad-client"}, Decision: policy.Deny, Reason: "this client may not"},
			{ID: "allow-rest", Decision: policy.Allow},
		}}}}
	return gatetest.Options{Policy: policy.NewEvaluator(file, "test"), Enforcement: enforceAll{}}
}

// The keys as the identity module verifies them (its own id as TokenID), and a token minted from
// `cerberus mcp`'s key (it names the key as Parent).
var (
	mcpKey      = libauth.Principal{Verified: true, ID: "key:m1", TokenID: "m1", Kind: libauth.KindService, Display: api.MCPKeyName, Scopes: stdioScopes}
	cliKey      = libauth.Principal{Verified: true, ID: "key:c1", TokenID: "c1", Kind: libauth.KindService, Display: api.CLIKeyName, Scopes: stdioScopes}
	otherKey    = libauth.Principal{Verified: true, ID: "key:o1", TokenID: "o1", Kind: libauth.KindService, Display: "tachyon", Scopes: stdioScopes}
	mintedToken = libauth.Principal{Verified: true, ID: "key:m1", TokenID: "jwt-1", Parent: "key:m1", Kind: libauth.KindService, Display: api.MCPKeyName, Scopes: stdioScopes}
)

// direct calls the API as p, with an optional MCP client header: what `cerberus mcp` and the
// CLI send over loopback.
func direct(t *testing.T, a *api.API, p libauth.Principal, header, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if header != "" {
		req.Header.Set(api.MCPClientHeader, header)
	}
	req = req.WithContext(libauth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	a.Mux().ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Body)
	return rec.Code, string(out)
}

func lastIntent(t *testing.T, w *gatetest.World) audit.Principal {
	t.Helper()
	recs := w.Records(audit.KindIntent)
	if len(recs) == 0 {
		t.Fatal("no intent recorded")
	}
	return recs[len(recs)-1].Principal
}

// The audit tells the channels apart, as old's did: HTTP /mcp is mcp_http with the MCP client's
// name; `cerberus mcp`'s key is mcp_stdio with the client it forwards; the CLI's key is cli; any
// other key is api, and a client header from it is ignored. Every one is an agent.
func TestAuditTellsMCPChannelsAndTheCLIApart(t *testing.T) {
	a, w := gatedAPIWith(t, clientPolicy())
	app := mcpApp(t)
	if err := cmcp.Register(app, cmcp.MuxBackend{Handler: a.Mux()}); err != nil {
		t.Fatal(err)
	}
	stop := `{"acknowledged":true}`

	// The stop of a production resource waits for the owner (the baseline); the gate records the
	// caller of every call either way.
	cs := connectNamed(t, app, stdioScopes, "claude-code")
	before := len(w.Records(audit.KindIntent))
	call(t, cs, "cerberus_resource_stop", map[string]any{"resource_id": "web", "acknowledged": true})
	if len(w.Records(audit.KindIntent)) == before {
		t.Fatal("the call over /mcp recorded no intent")
	}
	if p := lastIntent(t, w); p.Kind != "agent" || p.Via != "mcp_http" || p.Client != "claude-code" {
		t.Fatalf("over /mcp: %+v", p)
	}
	for _, c := range []struct {
		name        string
		p           libauth.Principal
		header      string
		via, client string
	}{
		{"cerberus mcp's key", mcpKey, "codex", "mcp_stdio", "codex"},
		{"cerberus mcp's key, no client named", mcpKey, "", "mcp_stdio", "mcp-client"},
		{"the CLI's key", cliKey, "", "cli", "cerberus-cli"},
		{"another key claiming a client", otherKey, "codex", "api", "key:o1"},
		{"a token minted from cerberus mcp's key", mintedToken, "codex", "api", "key:m1"},
	} {
		before := len(w.Records(audit.KindIntent))
		if code, body := direct(t, a, c.p, c.header, "POST", "/api/resources/web/stop", stop); code != 200 && code != http.StatusConflict {
			t.Fatalf("%s: %d %s", c.name, code, body)
		}
		if len(w.Records(audit.KindIntent)) == before {
			t.Fatalf("%s: no intent recorded", c.name)
		}
		if p := lastIntent(t, w); p.Kind != "agent" || p.Via != c.via || p.Client != c.client {
			t.Fatalf("%s: recorded %+v, want via %s client %s", c.name, p, c.via, c.client)
		}
	}
}

// A policy rule naming a client matches the MCP client, as on old; after two denials the circuit
// breaker suspends that MCP caller, and the CLI, another principal and channel, is not suspended.
func TestClientRulesAndTheBreakerAreKeyedPerChannel(t *testing.T) {
	a, _ := gatedAPIWith(t, clientPolicy())
	app := mcpApp(t)
	if err := cmcp.Register(app, cmcp.MuxBackend{Handler: a.Mux()}); err != nil {
		t.Fatal(err)
	}
	bad := connectNamed(t, app, stdioScopes, "bad-client")
	code := func(out string) string {
		var r struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal([]byte(out), &r)
		return r.Code
	}
	args := map[string]any{"resource_id": "web", "acknowledged": true}
	for i := range 2 {
		if out, isErr := call(t, bad, "cerberus_resource_stop", args); !isErr || code(out) != "policy_denied" {
			t.Fatalf("denial %d: %v %s", i+1, isErr, out)
		}
	}
	if out, isErr := call(t, bad, "cerberus_resource_stop", args); !isErr || code(out) != "session_suspended" {
		t.Fatalf("after two denials: %v %s", isErr, out)
	}
	if c, body := direct(t, a, cliKey, "", "POST", "/api/resources/web/stop", `{"acknowledged":true}`); strings.Contains(body, "session_suspended") || c == http.StatusLocked {
		t.Fatalf("the CLI after the MCP caller's suspension: %d %s", c, body)
	}
	// Approving is never an MCP caller's, even if the session behind /mcp is the owner's.
	owner := libauth.WithPrincipal(context.Background(), libauth.Principal{Verified: true, ID: "user:u1", Kind: "user", SessionID: "s1", Scopes: []string{"*"}})
	err := cmcp.MuxBackend{Handler: a.Mux()}.Do(owner, http.MethodPost, "/approvals/apr_x/decide", map[string]any{"approve": true}, nil)
	var r *cmcp.Refusal
	if !errors.As(err, &r) || r.Status != 403 || r.Code != "user_required" {
		t.Fatalf("an owner session through /mcp approving: %v", err)
	}
}
