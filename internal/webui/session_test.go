package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func withCookie(req *http.Request, c *http.Cookie) *http.Request {
	if c != nil {
		req.AddCookie(c)
	}
	return req
}

func sessionOf(t *testing.T, h http.Handler, c *http.Cookie) (int, string) {
	t.Helper()
	rec := serve(h, withCookie(newTestRequest(http.MethodGet, "/api/session", nil), c))
	var body struct {
		ActionToken string `json:"action_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.ActionToken
}

func loginPath(t *testing.T, srv *Server) string {
	t.Helper()
	u, err := srv.LoginURL("http://" + testHost)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(u, "http://"+testHost)
}

// Without a session the console hands out nothing: /api/session and every
// other API route answer 401 with the command that signs in, and that
// instruction survives redaction. The SPA shell itself still loads.
func TestUnauthenticatedCallerGetsNoToken(t *testing.T) {
	h := mustNew(t, &fakeClient{}).Handler(testGuard())
	for _, path := range []string{"/api/session", "/api/resources", "/api/overview"} {
		rec := serve(h, newTestRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "action_token") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		var body struct{ Message, Code string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.Message != loginRequired || body.Code != "login_required" {
			t.Fatalf("%s: body = %s", path, rec.Body.String())
		}
	}
	if redact.Text(loginRequired) != loginRequired {
		t.Fatalf("redaction ate the sign-in instruction: %q", redact.Text(loginRequired))
	}
	if rec := serve(h, newTestRequest(http.MethodGet, "/", nil)); rec.Code != http.StatusOK {
		t.Fatalf("SPA shell status = %d", rec.Code)
	}
}

// A sign-in link becomes a cookie that only the browser's own requests can
// carry, and the link's token leaves the address bar.
func TestLoginSetsAStrictHTTPOnlySessionCookie(t *testing.T) {
	srv := mustNew(t, &fakeClient{})
	h := srv.Handler(testGuard())
	rec := serve(h, newTestRequest(http.MethodGet, loginPath(t, srv), nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("login = %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers = %v", rec.Header())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Value == "" {
		t.Fatalf("cookie = %+v", cookie)
	}
	if code, token := sessionOf(t, h, cookie); code != http.StatusOK || token == "" {
		t.Fatalf("signed-in session = %d %q", code, token)
	}
}

// A link is good once, for its TTL, and only for the console that minted it.
func TestLoginLinksAreOneTimeShortLivedAndLocal(t *testing.T) {
	srv := mustNew(t, &fakeClient{})
	h := srv.Handler(testGuard())

	path := loginPath(t, srv)
	if rec := serve(h, newTestRequest(http.MethodGet, path, nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("first use = %d", rec.Code)
	}
	if rec := serve(h, newTestRequest(http.MethodGet, path, nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("second use = %d, want 401", rec.Code)
	}

	late := loginPath(t, srv)
	start := time.Now()
	srv.sessions.now = func() time.Time { return start.Add(DefaultLoginTTL + time.Second) }
	if rec := serve(h, newTestRequest(http.MethodGet, late, nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired link = %d, want 401", rec.Code)
	}
	srv.sessions.now = time.Now

	other := mustNew(t, &fakeClient{})
	if rec := serve(h, newTestRequest(http.MethodGet, loginPath(t, other), nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("another console's link = %d, want 401", rec.Code)
	}
	tampered := loginPath(t, srv)
	tampered = tampered[:len(tampered)-2] + "AA"
	if rec := serve(h, newTestRequest(http.MethodGet, tampered, nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered link = %d, want 401", rec.Code)
	}
	if rec := serve(h, newTestRequest(http.MethodGet, "/login", nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", rec.Code)
	}
}

// The Host and Origin guard still runs first, on /login as everywhere.
func TestLoginStaysBehindTheHostGuard(t *testing.T) {
	srv := mustNew(t, &fakeClient{})
	req := newTestRequest(http.MethodGet, loginPath(t, srv), nil)
	req.Host = "evil.example"
	if rec := serve(srv.Handler(testGuard()), req); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign Host = %d, want 403", rec.Code)
	}
}

func TestSessionsEndWhenIdleOrOld(t *testing.T) {
	srv := mustNew(t, &fakeClient{})
	h := srv.Handler(testGuard())
	start := time.Now()
	clock := start
	srv.sessions.now = func() time.Time { return clock }

	cookie := signIn(t, srv, h)
	clock = start.Add(DefaultSessionIdle - time.Minute)
	if code, _ := sessionOf(t, h, cookie); code != http.StatusOK {
		t.Fatalf("active session = %d", code)
	}
	clock = clock.Add(DefaultSessionIdle + time.Second)
	if code, _ := sessionOf(t, h, cookie); code != http.StatusUnauthorized {
		t.Fatalf("idle session = %d, want 401", code)
	}

	cookie = signIn(t, srv, h)
	for elapsed := time.Duration(0); elapsed <= DefaultSessionMax; elapsed += DefaultSessionIdle / 2 {
		clock = clock.Add(DefaultSessionIdle / 2)
		sessionOf(t, h, cookie)
	}
	if code, _ := sessionOf(t, h, cookie); code != http.StatusUnauthorized {
		t.Fatalf("session past its maximum age = %d, want 401", code)
	}
}

// The action token belongs to one session: it is refused with another
// session's cookie, and useless without a cookie at all.
func TestActionTokensArePerSession(t *testing.T) {
	srv := mustNew(t, &fakeClient{})
	h := srv.Handler(testGuard())
	a, b := signIn(t, srv, h), signIn(t, srv, h)
	_, tokenA := sessionOf(t, h, a)
	_, tokenB := sessionOf(t, h, b)
	if tokenA == "" || tokenA == tokenB {
		t.Fatalf("tokens = %q, %q", tokenA, tokenB)
	}
	post := func(c *http.Cookie, token string) int {
		req := withCookie(newTestRequest(http.MethodPost, "/api/resources/app/apply", strings.NewReader(`{"acknowledged":true}`)), c)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cerberus-Web-Token", token)
		return serve(h, req).Code
	}
	if code := post(b, tokenA); code != http.StatusForbidden {
		t.Fatalf("session A's token with session B's cookie = %d, want 403", code)
	}
	if code := post(nil, tokenA); code != http.StatusUnauthorized {
		t.Fatalf("a token with no cookie = %d, want 401", code)
	}
	if code := post(a, tokenA); code != http.StatusOK {
		t.Fatalf("session A's own token = %d, want 200", code)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	srv := mustNew(t, &fakeClient{})
	h := srv.Handler(testGuard())
	cookie := signIn(t, srv, h)
	_, token := sessionOf(t, h, cookie)

	bare := withCookie(newTestRequest(http.MethodPost, "/api/logout", nil), cookie)
	bare.Header.Set("Content-Type", "application/json")
	if code := serve(h, bare).Code; code != http.StatusForbidden {
		t.Fatalf("logout without the token = %d, want 403", code)
	}

	req := withCookie(newTestRequest(http.MethodPost, "/api/logout", nil), cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cerberus-Web-Token", token)
	rec := serve(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		cleared = cleared || (c.Name == sessionCookie && c.MaxAge < 0)
	}
	if !cleared {
		t.Fatal("logout did not clear the cookie")
	}
	if code, _ := sessionOf(t, h, cookie); code != http.StatusUnauthorized {
		t.Fatalf("session after logout = %d, want 401", code)
	}
}

// Restarting `cerberus web` is a new Server: every session and every link
// the old one handed out stops working.
func TestRestartInvalidatesSessionsAndLinks(t *testing.T) {
	before := mustNew(t, &fakeClient{})
	beforeHandler := before.Handler(testGuard())
	cookie := signIn(t, before, beforeHandler)
	link := loginPath(t, before)

	after := mustNew(t, &fakeClient{}).Handler(testGuard())
	if code, _ := sessionOf(t, after, cookie); code != http.StatusUnauthorized {
		t.Fatalf("old session after restart = %d, want 401", code)
	}
	if rec := serve(after, newTestRequest(http.MethodGet, link, nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old link after restart = %d, want 401", rec.Code)
	}
}

// `cerberus web open` mints from the key file the running console wrote:
// 0600 in a 0700 directory, removed on shutdown.
func TestWebOpenMintsFromThePrivateKeyFile(t *testing.T) {
	srv := mustNew(t, &fakeClient{})
	h := srv.Handler(testGuard())
	path := LoginKeyPath(t.TempDir(), "127.0.0.1:9090")
	remove, err := srv.WriteLoginKey(path, "http://"+testHost)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, statErr := os.Stat(p)
		if statErr != nil || info.Mode().Perm() != want {
			t.Fatalf("%s mode = %v, want %o (%v)", p, info.Mode().Perm(), want, statErr)
		}
	}
	u, err := MintLoginURL(path)
	if err != nil {
		t.Fatal(err)
	}
	if rec := serve(h, newTestRequest(http.MethodGet, strings.TrimPrefix(u, "http://"+testHost), nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("minted link = %d, want 303", rec.Code)
	}
	remove()
	if _, err := MintLoginURL(path); err == nil || !strings.Contains(err.Error(), "start it with `cerberus web`") {
		t.Fatalf("after shutdown: %v", err)
	}
}

// A signed-in request's principal is a human named by the session's public
// id — the id the audit record and the daemon's claim header carry, never the
// cookie's value.
func TestSignedInRequestsCarryTheSessionPrincipal(t *testing.T) {
	srv := mustNew(t, &fakeClient{})
	h := srv.Handler(testGuard())
	cookie := signIn(t, srv, h)

	var seen cerbapi.Principal
	capture := srv.requireSession(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = cerbapi.PrincipalFrom(r.Context())
	}))
	serve(capture, withCookie(newTestRequest(http.MethodGet, "/api/resources", nil), cookie))
	sess := srv.sessions.lookup(cookie.Value)
	if seen.Kind != cerbapi.PrincipalHuman || seen.Via != cerbapi.ViaWeb || seen.Session == "" || seen.Session != sess.ID || seen.SelfReported {
		t.Fatalf("principal = %+v, want a human web principal named by session %s", seen, sess.ID)
	}
	if seen.Session == cookie.Value {
		t.Fatal("the principal carries the cookie's value")
	}
}

// The console header shows the applied posture; the session carries it.
func TestSessionCarriesThePosture(t *testing.T) {
	srv := mustNew(t, &fakeClient{})
	srv.SetPosture(func() policy.PostureSummary {
		return policy.File{PostureRules: []policy.PostureRule{{Match: policy.TargetMatch{Env: "dev"}, Posture: policy.PosturePermissive}}}.PostureSummary("h")
	})
	h := srv.Handler(testGuard())
	rec := serve(h, withCookie(newTestRequest(http.MethodGet, "/api/session", nil), signIn(t, srv, h)))
	var body struct {
		Posture struct {
			Summary    string `json:"summary"`
			Permissive bool   `json:"permissive"`
		} `json:"posture"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Posture.Permissive || !strings.Contains(body.Posture.Summary, "permissive for env=dev") {
		t.Fatalf("session posture = %+v", body.Posture)
	}

	plain := mustNew(t, &fakeClient{})
	ph := plain.Handler(testGuard())
	rec = serve(ph, withCookie(newTestRequest(http.MethodGet, "/api/session", nil), signIn(t, plain, ph)))
	if !strings.Contains(rec.Body.String(), `"summary":"secure"`) {
		t.Fatalf("a console with no posture source must show secure: %s", rec.Body.String())
	}
}
