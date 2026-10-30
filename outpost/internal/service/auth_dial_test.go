package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	auth "github.com/hollis-labs/libs/security/auth"
)

func resolve(t *testing.T, addr, app, gen string) (auth.Mode, error) {
	t.Helper()
	t.Setenv("HOLLIS_AUTH_OUTPOST", app)
	t.Setenv("HOLLIS_AUTH", gen)
	return ResolveMode(addr)
}

func TestAuthDial(t *testing.T) {
	lo := "127.0.0.1:8090"
	if m, err := resolve(t, lo, "", ""); err != nil || m != auth.ModeEnforce {
		t.Fatalf("default: %v %v", m, err)
	}
	if m, err := resolve(t, lo, "off", ""); err != nil || m != auth.ModeOff {
		t.Fatalf("off: %v %v", m, err)
	}
	if m, err := resolve(t, lo, "", "permissive"); err != nil || m != auth.ModePermissive {
		t.Fatalf("permissive: %v %v", m, err)
	}
	if m, err := resolve(t, lo, "enforce", "off"); err != nil || m != auth.ModeEnforce {
		t.Fatalf("app var wins: %v %v", m, err)
	}
	if _, err := resolve(t, lo, "bogus", ""); err == nil {
		t.Fatal("invalid accepted")
	}
	for _, a := range []string{"100.64.0.5:8090", "0.0.0.0:8090", ":8090"} {
		for _, m := range []string{"off", "permissive"} {
			if _, err := resolve(t, a, m, ""); err == nil {
				t.Fatalf("%s on %s must be refused", m, a)
			}
		}
		if m, err := resolve(t, a, "enforce", ""); err != nil || m != auth.ModeEnforce {
			t.Fatalf("enforce on %s: %v %v", a, m, err)
		}
	}
}

func TestAuthorizePluginsModeFollowsMode(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	do := func(h http.Handler) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/plugins/cerberus/x", nil))
		return w.Code
	}
	if c := do(AuthorizePlugins(next)); c != 403 {
		t.Fatalf("enforce anonymous: %d", c)
	}
	for _, m := range []auth.Mode{auth.ModeOff, auth.ModePermissive} {
		if c := do(AuthorizePluginsMode(m, next)); c != 204 {
			t.Fatalf("%s anonymous: %d", m, c)
		}
	}
}
