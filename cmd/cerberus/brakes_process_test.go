package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// send calls the loopback child with a body, as a key (bearer) or as the owner's signed-in
// client (cookie, with the identity module's write headers).
func send(t *testing.T, client *http.Client, base, method, path, key, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body)) //nolint:noctx // loopback child
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Origin", base)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The kill switch end to end, with the real binary in supervise mode (scratch sandbox, fake
// systemctl): a read key cannot engage; the CLI engages a lockdown with its admin key; a
// mutation is then 423 lockdown and `status` leads with the banner; the key's lift is answered
// 409 approval_pending naming an out-of-band approval, which the key cannot approve itself and
// which lifts nothing while pending; the owner, signed in to the console, approves it there;
// the CLI then lifts with --approval <id>; the mutation is no longer braked; and the engage and
// the lift (noting the approval) are brake_changed records in the audit chain.
func TestLockdownEngagedByTheCLIAndLiftedWithTheOwnersApproval(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "cli.key")
	port := freePort(t)
	s := newSandbox(t, "httphost: { addr: \"127.0.0.1:"+port+"\" }\ncerberus: { mode: supervise, cli_key_file: \""+keyFile+"\" }\n")
	s.bin = filepath.Join(filepath.Dir(s.home), "bin")
	if err := os.MkdirAll(s.bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.bin, "systemctl"), []byte(fakeSystemctl), 0o700); err != nil { //nolint:gosec // the fake must run
		t.Fatal(err)
	}
	state := filepath.Join(s.home, ".cerberus")
	desc := filepath.Join(s.home, "apps", "demo.cerberus.yaml")
	for _, d := range []string{state, filepath.Dir(desc)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	descriptor := "kind: cerberus-project/v1\nowner: demo\nnamespace: local\nproject:\n  id: demo\n  name: Demo\nresources:\n" +
		"  - id: demo-dev\n    type: process\n    project: demo\n    connector: local\n    env: dev\n    owner: self\n    admin: self\n    config:\n      mode: dev_session\n      command: [\"/bin/sleep\", \"300\"]\n"
	registry := "version: 1\nentries:\n  - owner: demo\n    namespace: local\n    path: " + desc + "\n    kind: cerberus-project/v1\n"
	for p, content := range map[string]string{desc: descriptor, filepath.Join(state, "registry.yaml"): registry} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if code, out := runWithStdin(t, s, "correct horse battery staple\n", "identity", "setup", "--username", "owner", "--display-name", "Owner"); code != 0 {
		t.Fatalf("identity setup: %d %s", code, out)
	}
	if code, out := runWithStdin(t, s, "", "identity", "key", "create", "--name", "cli", "--scope", "cerberus.read", "--scope", "cerberus.operate", "--scope", "cerberus.admin", "--out", keyFile); code != 0 {
		t.Fatalf("key create: %d %s", code, out)
	}
	raw, err := os.ReadFile(keyFile) //nolint:gosec // the test's temp file
	if err != nil {
		t.Fatal(err)
	}
	admin := strings.TrimSpace(string(raw))
	read := mintKey(t, s, "read", "cerberus.read")

	cmd, base, log := start(t, s)
	plain := http.DefaultClient
	if code, body := send(t, plain, base, "POST", "/api/brakes/lockdown", read, `{}`); code != http.StatusForbidden {
		t.Fatalf("a read key engaging: %d %s", code, body)
	}
	var body string
	code, out := runWithStdin(t, s, "", "lockdown", "--reason", "drill")
	if code != 0 || !strings.HasPrefix(out, "LOCKDOWN ldn_") || !strings.Contains(out, "by agent over cli: drill") {
		t.Fatalf("cerberus lockdown: %d %s\n%s", code, out, log)
	}
	stop := func() (int, string) {
		return send(t, plain, base, "POST", "/api/resources/demo-dev/stop", admin, `{"acknowledged":true}`)
	}
	if code, body = stop(); code != http.StatusLocked || !strings.Contains(body, `"code":"lockdown"`) {
		t.Fatalf("a stop under the lockdown: %d %s", code, body)
	}
	if code, out = runWithStdin(t, s, "", "status"); code != 0 || !strings.HasPrefix(out, "!!! LOCKDOWN since ") {
		t.Fatalf("cerberus status: %d %s", code, out)
	}
	// Off a terminal the CLI does not even ask.
	if code, out = runWithStdin(t, s, "lift lockdown\n", "lockdown", "--off"); code == 0 || !strings.Contains(out, "interactive terminal") {
		t.Fatalf("cerberus lockdown --off: %d %s", code, out)
	}
	// The key's lift asks for the owner's approval.
	code, body = send(t, plain, base, "POST", "/api/brakes/lockdown/lift", admin, `{}`)
	var pending struct {
		Code    string `json:"code"`
		Details struct {
			Approval struct {
				ID      string `json:"id"`
				Channel string `json:"channel"`
			} `json:"approval"`
		} `json:"details"`
	}
	if code != http.StatusConflict || json.Unmarshal([]byte(body), &pending) != nil || pending.Code != "approval_pending" || pending.Details.Approval.ID == "" || pending.Details.Approval.Channel != "out_of_band" {
		t.Fatalf("a key asking to lift: %d %s\n%s", code, body, log)
	}
	id := pending.Details.Approval.ID
	if code, out = runWithStdin(t, s, "", "lockdown", "--off", "--approval", id); code == 0 || !strings.Contains(out, "not approved yet") {
		t.Fatalf("cerberus lockdown --off --approval on a pending approval: %d %s", code, out)
	}
	// No key approves it: cerberus.approve is held only by the owner's console session.
	if code, body = send(t, plain, base, "POST", "/api/approvals/"+id+"/decide", admin, `{"approve":true}`); code != http.StatusForbidden || !strings.Contains(body, "cerberus.approve") {
		t.Fatalf("the key approving its own lift: %d %s", code, body)
	}
	if code, body = stop(); code != http.StatusLocked {
		t.Fatalf("a stop before the owner approved: %d %s", code, body)
	}

	jar, _ := cookiejar.New(nil)
	owner := &http.Client{Jar: jar}
	if code, body = send(t, owner, base, "POST", "/api/login", "", `{"username":"owner","password":"correct horse battery staple"}`); code != 200 {
		t.Fatalf("login: %d %s", code, body)
	}
	if code, body = send(t, owner, base, "POST", "/api/approvals/"+id+"/decide", "", `{"approve":true}`); code != 200 {
		t.Fatalf("the owner approving: %d %s", code, body)
	}
	if code, out = runWithStdin(t, s, "", "lockdown", "--off", "--approval", id); code != 0 || !strings.Contains(out, "Lifted.") {
		t.Fatalf("cerberus lockdown --off --approval: %d %s\n%s", code, out, log)
	}
	if code, body = stop(); code == http.StatusLocked {
		t.Fatalf("a stop after the lift: %d %s", code, body)
	}
	// The list may lead with the sandbox audit chain's warnings, which the fresh server already
	// shows before any brake; the state is what counts.
	if code, out = runWithStdin(t, s, "", "freeze", "list"); code != 0 || !strings.HasSuffix(out, "No brake is engaged.\n") {
		t.Fatalf("cerberus freeze list: %d %s", code, out)
	}
	stopServe(t, cmd, log)

	var engaged, lifted bool
	_ = filepath.Walk(filepath.Join(s.data, "audit"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p) //nolint:gosec // the sandbox's own tree
			engaged = engaged || strings.Contains(string(b), `"note":"LOCKDOWN engaged: drill"`)
			lifted = lifted || strings.Contains(string(b), `"note":"LOCKDOWN lifted (approval:`+id+`)"`)
		}
		return nil
	})
	if !engaged || !lifted {
		t.Fatalf("the audit chain: engaged %v, lifted %v", engaged, lifted)
	}
}
