package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/cerberus/internal/loopback"
)

// signIn spends a one-time link against h and returns the session cookie.
func signIn(t *testing.T, srv *Server, h http.Handler) *http.Cookie {
	t.Helper()
	loginURL, err := srv.LoginURL("http://" + testHost)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newTestRequest(http.MethodGet, strings.TrimPrefix(loginURL, "http://"+testHost), nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if isSessionCookie(c) {
			rememberSessionKey(c, rec.Header().Get("Location"))
			return c
		}
	}
	t.Fatalf("login set no %s cookie", sessionCookie)
	return nil
}

// sessionKeys are the keys sign-ins handed their pages, by cookie value:
// what a browser's localStorage holds (H6).
var (
	sessionKeysMu sync.Mutex
	sessionKeys   = map[string]string{}
)

// rememberSessionKey keeps the key a login's redirect carried in its
// fragment, for the cookie it set.
func rememberSessionKey(c *http.Cookie, location string) {
	_, fragment, _ := strings.Cut(location, "#")
	values, _ := url.ParseQuery(fragment)
	sessionKeysMu.Lock()
	defer sessionKeysMu.Unlock()
	sessionKeys[c.Value] = values.Get(sessionKeyFragment)
}

func sessionKeyFor(c *http.Cookie) string {
	sessionKeysMu.Lock()
	defer sessionKeysMu.Unlock()
	return sessionKeys[c.Value]
}

// isSessionCookie is the console's session cookie, whatever port it names.
func isSessionCookie(c *http.Cookie) bool {
	return c.Name == sessionCookie || strings.HasPrefix(c.Name, sessionCookie+"_")
}

// signedIn is srv's handler behind guard for a browser that has signed in:
// every request that carries no session cookie of its own gets that one.
func signedIn(t *testing.T, srv *Server, guard *loopback.Guard) http.Handler {
	t.Helper()
	h := srv.Handler(guard)
	cookie := signIn(t, srv, h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasSessionCookie(r) {
			withCookie(r, cookie)
			if r.Header.Get(sessionKeyHeader) == "" {
				r.Header.Set(sessionKeyHeader, sessionKeyFor(cookie))
			}
		}
		h.ServeHTTP(w, r)
	})
}

func hasSessionCookie(r *http.Request) bool {
	for _, c := range r.Cookies() {
		if isSessionCookie(c) {
			return true
		}
	}
	return false
}
