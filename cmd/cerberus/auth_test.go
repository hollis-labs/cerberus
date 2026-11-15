package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
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

	// Approve is the owner's console session itself: it reaches the handler (501 until slice 6),
	// but a JWT minted from that session at /api/token is refused, though it names the owner.
	cookiePost := func(path, body string) (int, string) {
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(body)) //nolint:noctx // loopback child
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Origin", base)
		resp, err := owner.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	// The owner's session reaches the handler, which in observe mode holds no approvals.
	if code, body := cookiePost("/api/approvals/a1/decide", `{}`); code != 503 || !strings.Contains(body, "approvals_unavailable") {
		t.Fatalf("owner session on approve: %d %s, want the handler (503 approvals_unavailable in observe mode)", code, body)
	}
	code, body := cookiePost("/api/token", `{}`)
	var minted struct {
		Token string `json:"token"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &minted) != nil || minted.Token == "" {
		t.Fatalf("owner mints a token: %d %s", code, body)
	}
	if code, body := call(t, "POST", base+"/api/approvals/a1/decide", minted.Token); code != 403 || !strings.Contains(body, "user_required") {
		t.Fatalf("a token minted from the owner's session on approve: %d %s, want 403 user_required", code, body)
	}
}

// writeRegistry registers one synthetic descriptor in the sandbox's adopted root, in the old
// index format, so the legacy routes have a resource to answer about.
func writeRegistry(t *testing.T, s sandbox) {
	t.Helper()
	root := filepath.Join(s.home, ".cerberus")
	desc := filepath.Join(t.TempDir(), "shop.cerberus.yaml")
	if err := os.WriteFile(desc, []byte(`kind: cerberus-project/v1
owner: shop
project: {id: shop, name: Shop}
resources:
  - id: web
    name: Web
    type: process
    connector: local
    project: shop
    config: {command: ["./bin/web"], port: 8080}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	idx := "version: 1\nentries:\n  - owner: shop\n    namespace: default\n    path: " + desc +
		"\n    kind: cerberus-project/v1\n    registered_at: \"2026-01-02T03:04:05Z\"\n"
	if err := os.WriteFile(filepath.Join(root, "registry.yaml"), []byte(idx), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mintKey(t *testing.T, s sandbox, name string, scopes ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name+".key")
	args := []string{"identity", "key", "create", "--name", name, "--out", out}
	for _, sc := range scopes {
		args = append(args, "--scope", sc)
	}
	if code, msg := runWithStdin(t, s, "", args...); code != 0 {
		t.Fatalf("key create %s: %d %s", name, code, msg)
	}
	raw, err := os.ReadFile(out) //nolint:gosec // the test's temp file
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

// The legacy-route set (L1-L9) answers under /api on the loopback listener, each route behind
// its scope: a key without the scope gets 403, one with it reaches the route (reads 200,
// verbs 409 observe_mode). The /api mount is a cross-app contract (legacy-routes.md).
func TestLegacyRoutesAnswerUnderAPIWithTheirScopes(t *testing.T) {
	s := newSandbox(t, "")
	writeRegistry(t, s)
	if code, out := runWithStdin(t, s, "correct horse battery staple\n", "identity", "setup", "--username", "owner", "--display-name", "Owner"); code != 0 {
		t.Fatalf("identity setup: %d %s", code, out)
	}
	read := mintKey(t, s, "read", "cerberus.read")
	operate := mintKey(t, s, "operate", "cerberus.read", "cerberus.operate")
	admin := mintKey(t, s, "admin", "cerberus.read", "cerberus.operate", "cerberus.admin")

	cmd, base, log := start(t, s)
	defer stopServe(t, cmd, log)
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("the listener is not loopback: %s", base)
	}
	for _, c := range []struct {
		id, method, path, allowed, denied string
		want                              int
		body                              string
	}{
		{"L1", "GET", "/api/health", read, "", 200, `"daemon_running":true`},
		{"L2", "GET", "/api/resources", read, "", 200, `[{"id":"web"`},
		{"L3", "GET", "/api/resources/web/status", read, "", 200, `"id":"web"`},
		{"L4", "GET", "/api/resources/web/inspect", read, "", 200, `"id":"web"`},
		{"L5", "GET", "/api/resources/web/doctor", read, "", 200, `"resource_id":"web"`},
		{"L6", "GET", "/api/resources/web/logs?lines=5", read, "", 200, `"content":`},
		{"L7", "POST", "/api/resources/web/reload", operate, read, 409, `"observe_mode"`},
		{"L8", "POST", "/api/resources/web/remove", admin, operate, 409, `"observe_mode"`},
		{"L9", "GET", "/api/hosts", read, "", 200, `[]`},
	} {
		if code, body := call(t, c.method, base+c.path, ""); code != 401 {
			t.Errorf("%s %s %s without a key: %d %s, want 401", c.id, c.method, c.path, code, body)
		}
		if c.denied != "" {
			if code, body := call(t, c.method, base+c.path, c.denied); code != 403 {
				t.Errorf("%s %s %s without its scope: %d %s, want 403", c.id, c.method, c.path, code, body)
			}
		}
		if code, body := call(t, c.method, base+c.path, c.allowed); code != c.want || !strings.Contains(body, c.body) {
			t.Errorf("%s %s %s: %d %s, want %d containing %s", c.id, c.method, c.path, code, body, c.want, c.body)
		}
	}
}

// The CLI reads its key from cerberus.cli_key_file and calls the serving Cerberus over HTTP.
// Without the file it exits non-zero naming the path; a revoked key is refused (401) and shown.
func TestCLIReadsOverHTTPWithItsKeyFile(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "cli.key")
	// The sandbox's override sets a fixed loopback port so the CLI and the server agree.
	port := freePort(t)
	s := newSandbox(t, "httphost: { addr: \"127.0.0.1:"+port+"\" }\ncerberus: { cli_key_file: \""+keyFile+"\" }\n")
	writeRegistry(t, s)

	if code, out := runWithStdin(t, s, "", "resource", "list"); code == 0 || !strings.Contains(out, "no API key at "+keyFile) {
		t.Fatalf("without a key file: %d %s", code, out)
	}

	if code, out := runWithStdin(t, s, "correct horse battery staple\n", "identity", "setup", "--username", "owner", "--display-name", "Owner"); code != 0 {
		t.Fatalf("identity setup: %d %s", code, out)
	}
	if code, out := runWithStdin(t, s, "", "identity", "key", "create", "--name", "cli", "--scope", "cerberus.read", "--out", keyFile); code != 0 {
		t.Fatalf("key create: %d %s", code, out)
	}
	cmd, base, log := start(t, s)
	defer stopServe(t, cmd, log)

	if code, out := runWithStdin(t, s, "", "resource", "list"); code != 0 || !strings.Contains(out, "web  Web") {
		t.Fatalf("resource list: %d %s", code, out)
	}
	if code, out := runWithStdin(t, s, "", "resource", "status", "web"); code != 0 || !strings.Contains(out, "Resource:    web") {
		t.Fatalf("resource status: %d %s", code, out)
	}
	if code, out := runWithStdin(t, s, "", "status"); code != 0 || !strings.Contains(out, "  daemon   running at "+base) {
		t.Fatalf("status: %d %s", code, out)
	}

	// The owner revokes the key; the CLI then shows the server's 401.
	jar, _ := cookiejar.New(nil)
	owner := &http.Client{Jar: jar}
	send := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body)) //nolint:noctx // loopback child
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Origin", base)
		resp, err := owner.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := send("POST", "/api/login", `{"username":"owner","password":"correct horse battery staple"}`); code != 200 {
		t.Fatalf("login: %d %s", code, body)
	}
	code, body := send("GET", "/api/identity/keys", "")
	var listed struct {
		Keys []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"keys"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &listed) != nil {
		t.Fatalf("list keys: %d %s", code, body)
	}
	revoked := false
	for _, k := range listed.Keys {
		if k.Name == "cli" {
			if dc, db := send("DELETE", "/api/identity/keys/"+k.ID, ""); dc/100 != 2 {
				t.Fatalf("revoke: %d %s", dc, db)
			}
			revoked = true
		}
	}
	if !revoked {
		t.Fatalf("no key named cli in %s", body)
	}
	if code, out := runWithStdin(t, s, "", "resource", "list"); code == 0 || !strings.Contains(out, "401") || !strings.Contains(out, keyFile) {
		t.Fatalf("revoked key: %d %s", code, out)
	}
}

// freePort returns a loopback port nothing listens on now.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

// The CLI sends its key over plain http, so only to a loopback address.
func TestCLIRefusesANonLoopbackAddress(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "cli.key")
	if err := os.WriteFile(keyFile, []byte("key_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSandbox(t, "httphost: { addr: \"192.0.2.1:4786\" }\ncerberus: { cli_key_file: \""+keyFile+"\" }\n")
	if code, out := runWithStdin(t, s, "", "resource", "list"); code == 0 || !strings.Contains(out, "loopback") {
		t.Fatalf("non-loopback address: %d %s", code, out)
	}
}

// Help, version and the other commands not bound to the data start no module that opens or
// creates a file: under a read-only, empty HOME they succeed and HOME stays empty (the cutover
// runbook runs the new binary for checks on the operator's account).
func TestHelpAndVersionCreateNothing(t *testing.T) {
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o555); err != nil { //nolint:gosec // a read-only directory is the point
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) }) //nolint:gosec // a directory needs x to be removed
	for _, args := range [][]string{
		{"--help"}, {"-h"}, {"help"}, {"version"}, {"config"},
		{"serve", "--help"}, {"resource", "list", "--help"}, {"status", "--help"}, {"identity", "--help"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // the test binary
		cmd.Env = []string{"HOME=" + ro, "PATH=" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Errorf("cerberus %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		entries, _ := os.ReadDir(ro)
		if len(entries) != 0 {
			t.Fatalf("cerberus %s wrote under HOME: %v", strings.Join(args, " "), entries)
		}
	}
}

// needsData is the module plan: only serve (the default) and the offline identity commands
// start the modules that open the database and the signing key.
func TestNeedsData(t *testing.T) {
	for args, want := range map[string]bool{
		"": true, "serve": true, "identity setup --username o": true, "identity key create --name k": true,
		"--help": false, "serve --help": false, "version": false, "config": false, "health": false,
		"resource list": false, "resource status web": false, "status": false, "identity": false, "nonsense": false,
	} {
		if got := needsData(strings.Fields(args)); got != want {
			t.Errorf("needsData(%q) = %v, want %v", args, got, want)
		}
	}
}

// The plan reads every non-flag word as a command word, which holds only while the root has no
// global flag that takes a value.
func TestRootHasNoValueFlags(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--help") //nolint:gosec // the test binary
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	flags := strings.SplitN(string(out), "Flags:", 2)
	if len(flags) != 2 {
		t.Fatalf("no Flags section in:\n%s", out)
	}
	for _, line := range strings.Split(flags[1], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "-h, --help") {
			t.Errorf("root flag %q: needsData must learn to skip its value", line)
		}
	}
}
