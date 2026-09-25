package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// TestDNSRebindingIsRefused simulates a page on evil.example whose name has
// been re-pointed at 127.0.0.1. It reaches the listener, but every request
// carries Host: evil.example, and the Origin it sends matches that Host. Both
// used to pass: /api/session never checked Host, and the Origin check
// compared against r.Host.
func TestDNSRebindingIsRefused(t *testing.T) {
	client := &fakeClient{}
	handler := signedIn(t, mustNew(t, client), testGuard())
	token := sessionToken(t, handler)
	const evil = "evil.example:9090"

	for _, path := range []string{"/api/session", "/api/resources", "/", "/index.html"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = evil
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("GET %s with Host %s = %d, want 403", path, evil, rec.Code)
		}
		if strings.Contains(rec.Body.String(), token) {
			t.Fatalf("GET %s leaked the action token", path)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/resources/app/deploy", strings.NewReader("{}"))
	req.Host = evil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+evil)
	req.Header.Set("X-Cerberus-Web-Token", token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("rebinding POST = %d, want 403", rec.Code)
	}
	if client.deployCalls != 0 {
		t.Fatal("deploy ran under a rebinding Host")
	}
}

func TestLoopbackHostsServeTheConsole(t *testing.T) {
	handler := signedIn(t, mustNew(t, &fakeClient{}), testGuard())
	for _, host := range []string{"127.0.0.1:9090", "localhost:9090", "[::1]:9090", "localhost:5173"} {
		req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/session with Host %s = %d, want 200", host, rec.Code)
		}
	}
}

func TestMutationOriginMustMatchAllowedSet(t *testing.T) {
	for _, tc := range []struct {
		origin string
		want   int
	}{
		{"http://127.0.0.1:9090", http.StatusOK},
		{"http://localhost:9090", http.StatusOK},
		{"", http.StatusOK},
		{"http://evil.example:9090", http.StatusForbidden},
		{"http://127.0.0.1:1234", http.StatusForbidden},
		{"null", http.StatusForbidden},
	} {
		client := &fakeClient{}
		handler := signedIn(t, mustNew(t, client), testGuard())
		token := sessionToken(t, handler)
		req := newTestRequest(http.MethodPost, "/api/resources/app/stop", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cerberus-Web-Token", token)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("Origin %q: status = %d, want %d; body=%s", tc.origin, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// guardedRoutes lists, for every route that accepts a mutation, one request
// per mutation it serves. readOnlyRoutes lists the rest. Every pattern in
// routeTable must appear in exactly one, so a new route cannot arrive
// unclassified.
var guardedRoutes = map[string][]string{
	"/api/resources/": {
		"/api/resources/app/apply", "/api/resources/app/deploy", "/api/resources/app/reload",
		"/api/resources/app/stop", "/api/resources/app/sync", "/api/resources/app/remove",
	},
	"/api/logout":                 {"/api/logout"},
	"/api/pipelines/":             {"/api/pipelines/p/run"},
	"/api/registry/register":      {"/api/registry/register"},
	"/api/registry/deregister":    {"/api/registry/deregister"},
	"/api/config/migrate":         {"/api/config/migrate"},
	"/api/config/backups/restore": {"/api/config/backups/restore"},
	"/api/connectors/":            {"/api/connectors/c/operations/op"},
	"/api/infra/providers/":       {"/api/infra/providers/cloudflare"},
	"/api/deployments":            {"/api/deployments"},
	"/api/deployments/":           {"/api/deployments/d/delete", "/api/deployments/d/run"},
	"/api/plugins/connectors/": {
		"/api/plugins/connectors/p/load",
		"/api/plugins/connectors/p/unload", "/api/plugins/connectors/p/operations/op",
	},
}

// retiredRoutes answer 410 Gone to every request and do nothing: they used
// to take a plugin_dir, which a browser must not be able to hand the daemon.
// retiredPaths adds the one retired action under a live prefix.
var retiredRoutes = map[string]bool{
	"/api/plugins/connectors/health":      true,
	"/api/plugins/connectors/operations/": true,
}

var retiredPaths = map[string]bool{"/api/plugins/connectors/install": true}

func isRetired(path string) bool {
	if retiredPaths[path] {
		return true
	}
	for pattern := range retiredRoutes {
		if path == pattern || (strings.HasSuffix(pattern, "/") && strings.HasPrefix(path, pattern)) {
			return true
		}
	}
	return false
}

var readOnlyRoutes = map[string]bool{
	"/api/session": true, "/api/resources": true, "/api/overview": true,
	"/api/settings": true, "/api/system": true, "/api/health": true,
	"/api/projects": true, "/api/pipelines": true, "/api/registry": true,
	"/api/registry/health": true, "/api/config/validate": true,
	"/api/config/resolve": true, "/api/config/migrate/preview": true,
	"/api/config/backups": true, "/api/connectors": true, "/api/infra": true,
	"/api/plugins/connectors": true,
}

// actionWords are the path segments any handler dispatches on. The probe
// below tries them under every route, so a mutation added under an existing
// prefix without the guard is caught even if nobody lists it above.
var actionWords = []string{
	"apply", "deploy", "reload", "stop", "sync", "remove", "run", "delete",
	"install", "load", "unload", "health", "operations", "logs", "inspect",
	"doctor", "restore", "register", "deregister", "migrate", "preview",
}

func TestEveryRouteIsClassified(t *testing.T) {
	for _, rt := range mustNew(t, &fakeClient{}).routeTable() {
		_, guarded := guardedRoutes[rt.pattern]
		n := 0
		for _, in := range []bool{guarded, readOnlyRoutes[rt.pattern], retiredRoutes[rt.pattern]} {
			if in {
				n++
			}
		}
		if n != 1 {
			t.Errorf("route %s must be listed in exactly one of guardedRoutes, readOnlyRoutes or retiredRoutes", rt.pattern)
		}
	}
}

// TestEveryMutationGoesThroughAllowStateChangingRequest sends each listed
// mutation with a valid Host, a valid token and no Origin, but a text/plain
// body. Only allowStateChangingRequest checks Content-Type, so a 403 here
// proves the handler called it before acting.
func TestEveryMutationGoesThroughAllowStateChangingRequest(t *testing.T) {
	for pattern, paths := range guardedRoutes {
		for _, path := range paths {
			client := &fakeClient{}
			handler := signedIn(t, mustNew(t, client), testGuard())
			token := sessionToken(t, handler)

			for _, variant := range []struct {
				name  string
				setup func(*http.Request)
			}{
				{"no token", func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }},
				{"text/plain", func(r *http.Request) {
					r.Header.Set("Content-Type", "text/plain")
					r.Header.Set("X-Cerberus-Web-Token", token)
				}},
			} {
				req := newTestRequest(http.MethodPost, path, strings.NewReader("{}"))
				variant.setup(req)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusForbidden {
					t.Errorf("%s (%s) POST %s = %d, want 403; body=%s", pattern, variant.name, path, rec.Code, rec.Body.String())
				}
			}
			if client.calls() != 0 {
				t.Errorf("%s: client was called without a passing guard", path)
			}
		}
	}
}

// TestNoUnguardedMutationUnderAnyRoute probes every route, and every action
// word beneath it, with each state-changing method carrying a token but a
// text/plain body. A handler that acts without calling
// allowStateChangingRequest answers something other than 403, 404 or 405.
// Two other answers are known not to reach a handler's action: ServeMux's 307
// redirect to a pattern's trailing slash, and the id check on a bare
// /api/resources/.
func TestNoUnguardedMutationUnderAnyRoute(t *testing.T) {
	client := &fakeClient{}
	srv := mustNew(t, client)
	handler := signedIn(t, srv, testGuard())
	token := sessionToken(t, handler)

	var paths []string
	for _, rt := range srv.routeTable() {
		paths = append(paths, rt.pattern)
		if !strings.HasSuffix(rt.pattern, "/") {
			continue
		}
		paths = append(paths, rt.pattern+"x", rt.pattern+"x/y", rt.pattern+"x/y/z")
		for _, w := range actionWords {
			paths = append(paths, rt.pattern+w, rt.pattern+"x/"+w, rt.pattern+"x/"+w+"/op", rt.pattern+w+"/op")
		}
	}
	for _, path := range paths {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			req := newTestRequest(method, path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "text/plain")
			req.Header.Set("X-Cerberus-Web-Token", token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			switch {
			case rec.Code == http.StatusForbidden, rec.Code == http.StatusNotFound, rec.Code == http.StatusMethodNotAllowed:
			case rec.Code == http.StatusTemporaryRedirect:
			case rec.Code == http.StatusGone && isRetired(path):
			case rec.Code == http.StatusBadRequest && path == "/api/resources/":
			default:
				t.Errorf("%s %s = %d without passing the guard; body=%s", method, path, rec.Code, rec.Body.String())
			}
		}
	}
	if client.calls() != 0 {
		t.Errorf("client was called %d times without a passing guard", client.calls())
	}
}

// TestPluginDirRoutesAreRetiredOnTheWeb: the console can no longer preview,
// run or install a plugin directory, even with a valid token.
func TestPluginDirRoutesAreRetiredOnTheWeb(t *testing.T) {
	client := &fakeClient{}
	handler := signedIn(t, mustNew(t, client), testGuard())
	token := sessionToken(t, handler)
	for _, path := range []string{
		"/api/plugins/connectors/health",
		"/api/plugins/connectors/operations/logs",
		"/api/plugins/connectors/install",
	} {
		req := newTestRequest(http.MethodPost, path, strings.NewReader(`{"plugin_dir":"/tmp/evil"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cerberus-Web-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "in your shell") {
			t.Fatalf("POST %s = %d %s, want 410 naming the shell command", path, rec.Code, rec.Body.String())
		}
	}

	req := newTestRequest(http.MethodPost, "/api/plugins/connectors/p/operations/logs", strings.NewReader(`{"plugin_dir":"/tmp/evil"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cerberus-Web-Token", token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "plugin_dir is not accepted here") {
		t.Fatalf("managed exec with plugin_dir = %d %s, want 400 refusal", rec.Code, rec.Body.String())
	}
	if client.calls() != 0 {
		t.Fatalf("client called %d times", client.calls())
	}
}

func TestWebPluginInstallRefusalSurvivesRedaction(t *testing.T) {
	if got := redact.Text(webPluginInstallRetired); got != webPluginInstallRetired {
		t.Fatalf("redact.Text changed refusal:\n got %q\nwant %q", got, webPluginInstallRetired)
	}
}
