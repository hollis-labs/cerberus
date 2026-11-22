package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	auth "github.com/hollis-labs/libs/security/auth"
)

func TestPairingIsolationAndRevocation(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{CredentialsFile: filepath.Join(dir, "credentials.json"), Plugins: []Plugin{{ID: "cerberus"}, {ID: "tether"}}}
	path := filepath.Join(dir, "controller.token")
	if err := Pair(cfg, "controller", "cerberus", path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // Test-owned temporary credential output.
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(b))
	v := FileVerifier{Path: cfg.CredentialsFile}
	gate, err := auth.New(auth.Config{Mode: auth.ModeEnforce, Verifier: v})
	if err != nil {
		t.Fatal(err)
	}
	h := gate.Middleware(AuthorizePlugins(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	check := func(path, credential string, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", path, nil)
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: got %d, want %d: %s", path, w.Code, want, w.Body.String())
		}
	}
	check("/api/plugins/cerberus/execute", token, 204)
	check("/api/plugins/tether/status", token, 403)
	check("/api/plugins/cerberus/execute", "", 401)
	check("/api/plugins/cerberus/execute", "wrong", 401)
	if err := Revoke(cfg, "controller"); err != nil {
		t.Fatal(err)
	}
	check("/api/plugins/cerberus/execute", token, 401)
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("revoked credential verified")
	}
}

func TestPairDoesNotReplaceCredential(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{CredentialsFile: filepath.Join(dir, "credentials.json"), Plugins: []Plugin{{ID: "tether"}}}
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Pair(cfg, "controller", "tether", path); err == nil {
		t.Fatal("overwrote existing credential")
	}
	if err := Pair(cfg, "controller", "unknown", filepath.Join(dir, "new")); err == nil {
		t.Fatal("paired unknown plugin")
	}
}

func TestPublicPlaintextBindRefused(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0", "192.0.2.1:0", "localhost:0"} {
		if ln, err := Listen(addr); err == nil {
			ln.Close()
			t.Fatalf("accepted %s", addr)
		}
	}
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

func TestPairRejectsStoreAsOutput(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "pairings.json")
	cfg := Config{CredentialsFile: store, Plugins: []Plugin{{ID: "example"}}}
	if err := Pair(cfg, "controller", "example", store); err == nil {
		t.Fatal("accepted pairing store as token output")
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("refused pairing touched store: %v", err)
	}
}

func TestPairingLockPreventsLostUpdates(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{CredentialsFile: filepath.Join(dir, "pairings.json"), Plugins: []Plugin{{ID: "example"}}}
	unlock, err := pairingLock(cfg.CredentialsFile)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	output := filepath.Join(dir, "controller.token")
	if err = Pair(cfg, "controller", "example", output); err == nil {
		t.Fatal("paired while another writer held the store lock")
	}
	if _, err = os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("refused pairing wrote a token: %v", err)
	}
	unlock()
	if err = Pair(cfg, "controller", "example", output); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("token permissions = %o", info.Mode().Perm())
	}
}

func TestUnavailablePairingStoreFailsClosed(t *testing.T) {
	store := filepath.Join(t.TempDir(), "pairings.json")
	v := FileVerifier{Path: store}
	gate, err := auth.New(auth.Config{Mode: auth.ModeEnforce, Verifier: v})
	if err != nil {
		t.Fatal(err)
	}
	h := gate.Middleware(AuthorizePlugins(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unavailable verifier admitted a request")
	})))
	check := func() {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/plugins/example/execute", nil)
		r.Header.Set("Authorization", "Bearer invalid")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatalf("unavailable store returned %d", w.Code)
		}
	}
	check()
	if err := os.WriteFile(store, []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	check()
}
