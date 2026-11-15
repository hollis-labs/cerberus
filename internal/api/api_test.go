package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

// fixture resources: one supervised os_service, one dev session, one ssh handle.
func fixture() []config.ResourceDef {
	return []config.ResourceDef{
		{ID: "web", Name: "Web", Type: "process", Project: "shop", Connector: "local", Tags: []string{"API"},
			Config: map[string]any{"command": []any{"./bin/web"}, "port": 8080, "mode": "os_service", "supervisor": "systemd_user"}},
		{ID: "dev", Name: "Dev", Type: "process", Project: "shop", Connector: "local",
			Config: map[string]any{"command": []any{"npm", "run", "dev"}}},
		{ID: "box", Name: "Box", Type: "server", Project: "fleet", Connector: "ssh"},
	}
}

type fixedObserver struct{}

func (fixedObserver) Observe(_ context.Context, r config.ResourceDef) Observation {
	if r.ID == "web" {
		return Observation{State: "running", Unit: "com.hollis-labs.cerberus.shop.web.service"}
	}
	return Observation{State: "stopped"}
}

func newTestAPI(t *testing.T, hostsYAML string) *API {
	t.Helper()
	dir := t.TempDir()
	hp := filepath.Join(dir, "hosts.yaml")
	if hostsYAML != "" {
		if err := os.WriteFile(hp, []byte(hostsYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a, err := New(Options{
		Resources: func() ([]config.ResourceDef, error) { return fixture(), nil },
		Observer:  fixedObserver{},
		Hosts:     FileHosts{HostsPath: hp, StatePath: filepath.Join(dir, "state", "hosts.json")},
		Home:      dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func do(t *testing.T, h http.Handler, method, path string, hdr map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

// Every route has a known scope and is mapped in the table the guard enforces, by the same list
// the mux is built from; a path nobody declared is unmapped (the guard refuses it).
func TestEveryRouteHasAScope(t *testing.T) {
	a := newTestAPI(t, "")
	table := libauth.NewScopeTable()
	a.RegisterScopes(table)
	if len(a.Routes()) == 0 {
		t.Fatal("no routes")
	}
	for _, rt := range a.Routes() {
		concrete := strings.NewReplacer("{id}", "x", "{action}", "nope").Replace(rt.Pattern)
		got, known := table.ForRoute(rt.Method, concrete)
		if !known || got != rt.Scope {
			t.Errorf("%s %s: scope %q known=%v, want %q", rt.Method, concrete, got, known, rt.Scope)
		}
	}
	for _, p := range [][2]string{{"GET", "/api/admin"}, {"DELETE", "/api/resources/x/status"}, {"PUT", "/api/hosts"}} {
		if _, known := table.ForRoute(p[0], p[1]); known {
			t.Errorf("%s %s is mapped but not declared", p[0], p[1])
		}
	}
	// The verbs carry the plan's scopes: remove is admin, the rest operate, reads read.
	for path, want := range map[string]string{
		"/api/resources/x/reload": ScopeOperate, "/api/resources/x/remove": ScopeAdmin,
		"/api/approvals/x/decide": ScopeApprove,
	} {
		if got, _ := table.ForRoute(http.MethodPost, path); got != want {
			t.Errorf("POST %s: scope %q, want %q", path, got, want)
		}
	}
}

// A route added without a scope (or with an unknown one) stops the start.
func TestValidateRefusesARouteWithoutAScope(t *testing.T) {
	ok := func(http.ResponseWriter, *http.Request) {}
	for _, c := range []struct {
		r    Route
		want string
	}{
		{Route{http.MethodGet, "/api/new", "", ok}, `scope ""`},
		{Route{http.MethodGet, "/api/new", "cerberus.everything", ok}, `scope "cerberus.everything"`},
		{Route{http.MethodGet, "/new", ScopeRead, ok}, "outside /api/"},
		{Route{http.MethodGet, "/api/new", ScopeRead, nil}, "no handler"},
	} {
		if err := Validate([]Route{c.r}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%s %s %q) = %v, want %q", c.r.Method, c.r.Pattern, c.r.Scope, err, c.want)
		}
	}
	dup := Route{http.MethodGet, "/api/x", ScopeRead, ok}
	if err := Validate([]Route{dup, dup}); err == nil {
		t.Error("a repeated route was accepted")
	}
}

func TestLegacyReads(t *testing.T) {
	h := newTestAPI(t, `version: 1
hosts:
  - name: box-1
    role: worker
    provider: homelab
    labels: {rack: a}
  - name: box-2
    role: service
`).Mux()

	code, body := do(t, h, "GET", "/api/resources", nil)
	var list []map[string]any
	if code != 200 || json.Unmarshal([]byte(body), &list) != nil || len(list) != 3 {
		t.Fatalf("GET /api/resources = %d %s", code, body)
	}
	if list[0]["id"] != "web" || list[0]["status"] != "running" || list[2]["status"] != "unsupervised" || list[0]["admin"] != "unknown" {
		t.Fatalf("rows = %s", body)
	}
	if code, body = do(t, h, "GET", "/api/resources?project_id=fleet", nil); code != 200 || !strings.HasPrefix(body, `[{"id":"box"`) {
		t.Fatalf("project filter = %d %s", code, body)
	}
	if code, body = do(t, h, "GET", "/api/resources?tag=api", nil); code != 200 || !strings.HasPrefix(body, `[{"id":"web"`) {
		t.Fatalf("tag filter (case-insensitive) = %d %s", code, body)
	}
	if code, body = do(t, h, "GET", "/api/resources?connector=docker", nil); code != 200 || body != "[]\n" {
		t.Fatalf("an empty list must be [] = %d %q", code, body)
	}

	code, body = do(t, h, "GET", "/api/resources/web/status", nil)
	if code != 200 || !strings.Contains(body, `"status":"running"`) || !strings.Contains(body, `"service_name":"com.hollis-labs.cerberus.shop.web.service"`) {
		t.Fatalf("status = %d %s", code, body)
	}
	if code, body = do(t, h, "GET", "/api/resources/box/status", nil); code != 200 || !strings.Contains(body, `"recommended_next_step":"cerberus ssh status box`) {
		t.Fatalf("unsupervised status = %d %s", code, body)
	}
	for _, p := range []string{"/api/resources/web/inspect", "/api/resources/web/doctor", "/api/resources/web/logs?lines=5"} {
		if code, body = do(t, h, "GET", p, nil); code != 200 {
			t.Fatalf("GET %s = %d %s", p, code, body)
		}
	}
	if !strings.Contains(body, `"content":"Log file does not exist: `) {
		t.Fatalf("logs for a missing file = %s", body)
	}

	if code, body = do(t, h, "GET", "/api/resources/nope/status", nil); code != 404 || !strings.Contains(body, `"code":"not_found"`) {
		t.Fatalf("unknown resource = %d %s", code, body)
	}
	if code, body = do(t, h, "GET", "/api/resources/web/frobnicate", nil); code != 404 || !strings.Contains(body, `unknown resource action \"frobnicate\"`) {
		t.Fatalf("unknown action = %d %s", code, body)
	}

	code, body = do(t, h, "GET", "/api/health", nil)
	if code != 200 || !strings.HasPrefix(body, `{"services":null,"resources":[`) || !strings.Contains(body, `"daemon_running":true,"services_protected":0,"services_failed":0}`) {
		t.Fatalf("health = %d %s", code, body)
	}
	if code, body = do(t, h, "GET", "/api/health?resource_id=nope", nil); code != 200 || strings.Contains(body, `"resources"`) {
		t.Fatalf("health for an unknown id omits resources = %d %s", code, body)
	}

	code, body = do(t, h, "GET", "/api/hosts", nil)
	if code != 200 || !strings.HasPrefix(body, `[{"host":{"name":"box-1"`) || !strings.Contains(body, `"state":"unknown"`) || !strings.Contains(body, `"stale":true`) {
		t.Fatalf("hosts = %d %s", code, body)
	}
	if code, body = do(t, h, "GET", "/api/hosts?label=rack=a", nil); code != 200 || strings.Contains(body, "box-2") {
		t.Fatalf("label filter = %d %s", code, body)
	}
	if code, body = do(t, h, "GET", "/api/hosts?label=rack", nil); code != 400 || !strings.Contains(body, "a label filter is key=value") {
		t.Fatalf("bad label filter = %d %s", code, body)
	}
}

func TestLegacyVersionHeaderAndObserveMode(t *testing.T) {
	h := newTestAPI(t, "").Mux()
	code, body := do(t, h, "GET", "/api/resources", map[string]string{"X-Cerberus-Api": "v2"})
	if code != 400 || !strings.Contains(body, "unsupported X-Cerberus-Api: v2 (server expects v1)") {
		t.Fatalf("v2 = %d %s", code, body)
	}
	req := httptest.NewRequest("GET", "/api/resources", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cerberus-Api") != "v1" {
		t.Fatal("the answer does not name v1")
	}
	for _, verb := range []string{"reload", "deploy", "remove"} {
		code, body = do(t, h, "POST", "/api/resources/web/"+verb, nil)
		if code != 409 || !strings.Contains(body, `"code":"observe_mode"`) {
			t.Fatalf("POST %s = %d %s", verb, code, body)
		}
	}
	// L7 and L8 have no 404 (legacy-routes.md): in observe mode every id is refused alike.
	if code, body = do(t, h, "POST", "/api/resources/nope/reload", nil); code != 409 || !strings.Contains(body, `"code":"observe_mode"`) {
		t.Fatalf("a verb on an unknown resource = %d %s", code, body)
	}
}

// Approving needs a signed-in person in a console session: an API key, a token minted from one,
// a person without a session, and a local-trust principal all get 403 user_required, even when
// they hold the scope (the guard has already checked it).
func TestApproveIsPersonOnly(t *testing.T) {
	h := newTestAPI(t, "").Mux()
	post := func(p *libauth.Principal) (int, string) {
		req := httptest.NewRequest("POST", "/api/approvals/a1/decide", bytes.NewReader([]byte(`{}`)))
		if p != nil {
			req = req.WithContext(libauth.WithPrincipal(req.Context(), *p))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	all := []string{"*"}
	for name, p := range map[string]*libauth.Principal{
		"no principal":                  nil,
		"api key holding *":             {Verified: true, ID: "key:k1", Kind: libauth.KindService, Scopes: all},
		"token minted from a key":       {Verified: true, ID: "key:k1", Kind: libauth.KindService, Scopes: all, TokenID: "t1", Parent: "key:k1"},
		"local trust":                   {Verified: true, ID: "local:operator", Kind: libauth.KindOperator, Scopes: all},
		"person without a session":      {Verified: true, ID: "user:u1", Kind: "user", Scopes: all},
		"token minted from the session": {Verified: true, ID: "user:u1", Kind: "user", SessionID: "s1", TokenID: "tok_1", Scopes: all},
		"token with a parent":           {Verified: true, ID: "user:u1", Kind: "user", SessionID: "s1", Parent: "user:u1", Scopes: all},
		"session principal, wrong kind": {Verified: true, ID: "user:u1", Kind: libauth.KindOperator, SessionID: "s1", Scopes: all},
		"unverified person in session":  {ID: "user:u1", SessionID: "s1", Scopes: all},
	} {
		if code, body := post(p); code != 403 || !strings.Contains(body, `"code":"user_required"`) {
			t.Errorf("%s: %d %s, want 403 user_required", name, code, body)
		}
	}
	owner := &libauth.Principal{Verified: true, ID: "user:u1", Kind: "user", SessionID: "s1", Scopes: all}
	if code, body := post(owner); code != 501 {
		t.Errorf("owner in a console session: %d %s, want to reach the handler (501 until slice 6)", code, body)
	}
}

// No machine credential may be minted holding approve, directly or through a wildcard.
func TestKeyGuardRefusesApproveOnKeys(t *testing.T) {
	reached := false
	h := KeyGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	}))
	for _, path := range []string{"/api/identity/keys", "/api/identity/pairing-codes"} {
		for _, scopes := range []string{`["*"]`, `["cerberus.*"]`, `["cerberus.read","cerberus.approve"]`, `[" cerberus.approve "]`} {
			reached = false
			req := httptest.NewRequest("POST", path, strings.NewReader(`{"name":"k","scopes":`+scopes+`}`))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 403 || reached || !strings.Contains(rec.Body.String(), `"code":"scope_not_grantable"`) {
				t.Errorf("POST %s %s = %d reached=%v %s", path, scopes, rec.Code, reached, rec.Body.String())
			}
		}
		reached = false
		body := `{"name":"k","scopes":["cerberus.read","cerberus.operate","cerberus.admin"]}`
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if !reached || rec.Body.String() != body {
			t.Errorf("POST %s with grantable scopes: reached=%v body=%q (the body must pass through unchanged)", path, reached, rec.Body.String())
		}
	}
}
