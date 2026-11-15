package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/target"
)

// memKeyring is an in-memory credential store: these tests never reach the OS keyring.
type memKeyring map[string]string

func (m memKeyring) Get(_ context.Context, service, key string) (string, error) {
	return m[service+"/"+key], nil
}
func (m memKeyring) Set(_ context.Context, service, key, value string) error {
	m[service+"/"+key] = value
	return nil
}
func (m memKeyring) Delete(_ context.Context, service, key string) error {
	delete(m, service+"/"+key)
	return nil
}

// adminAPI is the API with an admin lane over a scratch adopted root and a production ssh host.
func adminAPI(t *testing.T) (*gatedAPI, *API) {
	t.Helper()
	w := gatetest.New(t, gatetest.Options{})
	box := config.ResourceDef{ID: "box", Name: "box", Type: "server", Connector: "ssh", Env: target.EnvProd, Owner: target.OwnerSelf,
		Admin: target.Admin{Default: target.AdminSelf}, Config: map[string]any{"host": "box.invalid", "key_file": "/keys/box"}}
	root := filepath.Join(t.TempDir(), "adopted")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	svc, err := admin.New(admin.Options{Gate: w.Gate, Paths: admin.Paths{AdoptRoot: root, ConfigDir: t.TempDir()}, Keyring: memKeyring{},
		Resources: func(id string) (*config.ResourceDef, bool) {
			if id != box.ID {
				return nil, false
			}
			return &box, true
		}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return []config.ResourceDef{box}, nil }, Home: t.TempDir(), Gate: w.Gate, Admin: svc})
	if err != nil {
		t.Fatal(err)
	}
	return &gatedAPI{h: a.Mux(), w: w}, a
}

// Observe mode runs no admin lane: every admin route answers 503 admin_unavailable.
func TestAdminRoutesInObserveMode(t *testing.T) {
	a := newTestAPI(t, "")
	for _, rt := range a.adminRoutes() {
		path := strings.NewReplacer("{id}", "echo", "{op}", "echo").Replace(rt.Pattern)
		req := httptest.NewRequest(rt.Method, path, strings.NewReader(`{}`))
		req = req.WithContext(libauth.WithPrincipal(req.Context(), ownerSession))
		rec := httptest.NewRecorder()
		a.Mux().ServeHTTP(rec, req)
		wantCode(t, rt.Method+" "+path, rec.Code, rec.Body.String(), http.StatusServiceUnavailable, "admin_unavailable")
	}
}

// The routes carry the contract's scopes: every GET is cerberus.read, every write
// cerberus.admin, and every gated write has its /plan and /confirm forms.
func TestAdminRoutesHaveTheContractsScopes(t *testing.T) {
	a := newTestAPI(t, "")
	forms := map[string]int{}
	for _, rt := range a.adminRoutes() {
		want := ScopeAdmin
		if rt.Method == http.MethodGet {
			want = ScopeRead
		}
		if rt.Scope != want {
			t.Errorf("%s %s has scope %s, want %s", rt.Method, rt.Pattern, rt.Scope, want)
		}
		base := strings.TrimSuffix(strings.TrimSuffix(rt.Pattern, "/plan"), "/confirm")
		forms[base]++
	}
	for _, base := range []string{"/api/connectors/{id}/operations/{op}", "/api/credentials/{id}", "/api/plugins/connectors/{id}/load", "/api/plugins/connectors/{id}/unload"} {
		if forms[base] != 3 {
			t.Errorf("%s has %d forms, want the base, /plan and /confirm", base, forms[base])
		}
	}
	for _, rt := range a.Routes() {
		if p := strings.ReplaceAll(rt.Pattern, "uninstall", ""); strings.Contains(p, "install") || strings.Contains(p, "upgrade") || strings.Contains(p, "review") {
			t.Errorf("%s %s: plugin install and review are not served until the ruling", rt.Method, rt.Pattern)
		}
	}
}

// A connector operation's forms: the plan alone runs nothing and answers the ConnectorPlan; a
// confirm without a hash is 400 and with another plan's hash 409 plan_stale; an API key's exec
// on a production host is 409 approval_pending naming the approval.
func TestConnectorOperationForms(t *testing.T) {
	g, _ := adminAPI(t)
	body := `{"config":{"id":"box","command":"uptime"},"acknowledged":true}`
	code, out := g.call(t, ownerSession, "POST", "/api/connectors/ssh/operations/exec/plan", body)
	var shown gate.ConnectorPlan
	if code != http.StatusOK || json.Unmarshal([]byte(out), &shown) != nil || shown.PlanHash == "" || shown.Plan.Operation != "exec" {
		t.Fatalf("plan form: %d %s", code, out)
	}
	code, out = g.call(t, ownerSession, "POST", "/api/connectors/ssh/operations/exec/confirm", body)
	wantCode(t, "confirm without a hash", code, out, http.StatusBadRequest, "invalid_args")
	code, out = g.call(t, ownerSession, "POST", "/api/connectors/ssh/operations/exec/confirm", `{"config":{"id":"box","command":"uptime"},"acknowledged":true,"confirmed_plan_hash":"sha256:abc"}`)
	wantCode(t, "confirm with another plan", code, out, http.StatusConflict, "plan_stale")
	id := g.pendingID(t, apiKey, "POST", "/api/connectors/ssh/operations/exec", body)
	if code, out = g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != http.StatusOK {
		t.Fatalf("the owner's approval: %d %s", code, out)
	}
	// The key's retry naming the approval passes the gate (the host is unreachable, so the run
	// itself fails past it), and spends the approval.
	retry := `{"config":{"id":"box","command":"uptime"},"acknowledged":true,"approval_id":"` + id + `"}`
	code, out = g.call(t, apiKey, "POST", "/api/connectors/ssh/operations/exec", retry)
	if code != http.StatusBadGateway || !strings.Contains(out, `"code":"operation_failed"`) {
		t.Fatalf("the retry under the approval did not pass the gate: %d %s", code, out)
	}
	code, out = g.call(t, ownerSession, "POST", "/api/connectors/nope/operations/exec", body)
	wantCode(t, "an unknown connector", code, out, http.StatusNotFound, "operation_unsupported")
}

// The reads: the connectors' definitions, the credential editor (names and presence), the
// managed plugins; a provider save of an undeclared connector is 400 and writes nothing.
func TestAdminReads(t *testing.T) {
	g, _ := adminAPI(t)
	code, out := g.call(t, apiKey, "GET", "/api/connectors", "")
	if code != http.StatusOK || !strings.Contains(out, `"id":"ssh"`) || !strings.Contains(out, `"id":"docker"`) {
		t.Fatalf("GET /api/connectors: %d %s", code, out)
	}
	code, out = g.call(t, apiKey, "GET", "/api/credentials", "")
	if code != http.StatusOK || !strings.Contains(out, `"providers":[`) {
		t.Fatalf("GET /api/credentials: %d %s", code, out)
	}
	code, out = g.call(t, apiKey, "GET", "/api/plugins/connectors", "")
	if code != http.StatusOK || strings.TrimSpace(out) != "[]" {
		t.Fatalf("GET /api/plugins/connectors: %d %s", code, out)
	}
	code, out = g.call(t, ownerSession, "POST", "/api/credentials/nope", `{"secrets":{"token":"x"}}`)
	wantCode(t, "a save for an undeclared connector", code, out, http.StatusBadRequest, "invalid_args")
	code, out = g.call(t, ownerSession, "POST", "/api/plugins/connectors/echo/operations/echo", `{"plugin_dir":"/tmp/x"}`)
	wantCode(t, "a plugin_dir on the plugin route", code, out, http.StatusBadRequest, "invalid_args")
}
