package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("login set no %s cookie", sessionCookie)
	return nil
}

// signedIn is srv's handler behind guard for a browser that has signed in:
// every request that carries no session cookie of its own gets that one.
func signedIn(t *testing.T, srv *Server, guard *loopback.Guard) http.Handler {
	t.Helper()
	h := srv.Handler(guard)
	cookie := signIn(t, srv, h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(sessionCookie); err != nil {
			r.AddCookie(cookie)
		}
		h.ServeHTTP(w, r)
	})
}
