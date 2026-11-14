package main

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runWithStdin runs cerberus to completion with stdin, and returns its exit code and output.
func runWithStdin(t *testing.T, s sandbox, stdin string, args ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := s.command(ctx, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(interface{ ExitCode() int }); ok {
			return ee.ExitCode(), string(out)
		}
		t.Fatal(err)
	}
	return 0, string(out)
}

func call(t *testing.T, method, url, key string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader("{}")) //nolint:noctx // test against the loopback child
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// With identity and auth in enforce mode: no key is 401, a read key reads the legacy routes
// and nothing else, a key cannot be minted with approve or a wildcard covering it, and a key
// never reaches the approve routes.
func TestAuthEnforcedEndToEnd(t *testing.T) {
	s := newSandbox(t, "")
	if code, out := runWithStdin(t, s, "correct horse battery staple\n", "identity", "setup", "--username", "owner", "--display-name", "Owner"); code != 0 {
		t.Fatalf("identity setup: %d %s", code, out)
	}
	for _, scope := range []string{"*", "cerberus.*", "cerberus.approve"} {
		out := filepath.Join(t.TempDir(), "refused.key")
		code, msg := runWithStdin(t, s, "", "identity", "key", "create", "--name", "bad", "--scope", scope, "--out", out)
		if code == 0 || !strings.Contains(msg, "cerberus.approve") {
			t.Fatalf("key create --scope %s: %d %s, want a refusal naming cerberus.approve", scope, code, msg)
		}
		if _, err := os.Stat(out); err == nil {
			t.Fatalf("a refused key create wrote %s", out)
		}
	}
	keyFile := filepath.Join(t.TempDir(), "reader.key")
	if code, out := runWithStdin(t, s, "", "identity", "key", "create", "--name", "reader", "--scope", "cerberus.read", "--out", keyFile); code != 0 {
		t.Fatalf("key create: %d %s", code, out)
	}
	raw, err := os.ReadFile(keyFile) //nolint:gosec // the test's temp file
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(string(raw))

	cmd, base, log := start(t, s)
	defer stopServe(t, cmd, log)

	if code, body := call(t, "GET", base+"/api/resources", ""); code != 401 {
		t.Fatalf("no key: %d %s", code, body)
	}
	if code, body := call(t, "GET", base+"/api/resources", key); code != 200 || body != "[]\n" {
		t.Fatalf("read key on /api/resources: %d %q", code, body)
	}
	if code, body := call(t, "GET", base+"/api/hosts", key); code != 200 || body != "[]\n" {
		t.Fatalf("read key on /api/hosts: %d %q", code, body)
	}
	if code, body := call(t, "POST", base+"/api/resources/web/reload", key); code != 403 {
		t.Fatalf("read key on a verb: %d %s, want 403", code, body)
	}
	if code, body := call(t, "POST", base+"/api/approvals/a1/decide", key); code != 403 {
		t.Fatalf("read key on approve: %d %s, want 403", code, body)
	}
	if code, body := call(t, "GET", base+"/api/not-a-route", key); code != 403 || !strings.Contains(body, "route_not_mapped") {
		t.Fatalf("an undeclared route: %d %s, want 403 route_not_mapped", code, body)
	}
	if code, body := call(t, "GET", base+"/healthz", ""); code != 200 {
		t.Fatalf("/healthz stays public: %d %s", code, body)
	}

	// The owner, signed in, cannot mint a key holding approve over HTTP either.
	jar, _ := cookiejar.New(nil)
	owner := &http.Client{Jar: jar}
	login, _ := http.NewRequest("POST", base+"/api/login", strings.NewReader(`{"username":"owner","password":"correct horse battery staple"}`)) //nolint:noctx // loopback child
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("X-Requested-With", "XMLHttpRequest")
	login.Header.Set("Origin", base)
	resp, err := owner.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("owner login: %d", resp.StatusCode)
	}
	for _, scopes := range []string{`["*"]`, `["cerberus.approve"]`} {
		mint, _ := http.NewRequest("POST", base+"/api/identity/keys", strings.NewReader(`{"name":"k","scopes":`+scopes+`}`)) //nolint:noctx // loopback child
		mint.Header.Set("Content-Type", "application/json")
		mint.Header.Set("X-Requested-With", "XMLHttpRequest")
		mint.Header.Set("Origin", base)
		resp, err := owner.Do(mint)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 403 || !strings.Contains(string(b), "scope_not_grantable") {
			t.Fatalf("owner minting %s: %d %s", scopes, resp.StatusCode, b)
		}
	}
}
