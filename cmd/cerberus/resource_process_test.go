package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Runbook D7, exactly: `$NEW resource apply <id> --ack` with the real binary against a
// supervise-mode server (scratch sandbox, fake systemctl on PATH), over HTTP with the CLI's key.
// Without --ack it is refused as old refused it and nothing starts; with it the dev session
// starts (its pid file in the data directory names a live process) and the CLI prints old's
// line. `resource stop <id> --ack` then stops it. systemctl is never asked to start anything.
func TestRunbookD7ResourceApplyStartsTheDevSession(t *testing.T) {
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
	if err := os.WriteFile(desc, []byte(descriptor), 0o600); err != nil {
		t.Fatal(err)
	}
	// The registry is written by the new CLI's own register, as tether sysop does.
	if code, out := runWithStdin(t, s, "", "register", desc); code != 0 || !strings.Contains(out, "registered: demo [cerberus-project/v1]") {
		t.Fatalf("cerberus register: %d %s", code, out)
	}
	if code, out := runWithStdin(t, s, "correct horse battery staple\n", "identity", "setup", "--username", "owner", "--display-name", "Owner"); code != 0 {
		t.Fatalf("identity setup: %d %s", code, out)
	}
	if code, out := runWithStdin(t, s, "", "identity", "key", "create", "--name", "cli", "--scope", "cerberus.read", "--scope", "cerberus.operate", "--out", keyFile); code != 0 {
		t.Fatalf("key create: %d %s", code, out)
	}
	cmd, _, log := start(t, s)
	pidFile := filepath.Join(s.data, "pids", "demo-dev.pid")

	code, out := runWithStdin(t, s, "", "resource", "apply", "demo-dev")
	if code == 0 || !strings.Contains(out, "409 acknowledgment_required") || !strings.Contains(out, "run it again with --ack") {
		t.Fatalf("apply without --ack: %d %s", code, out)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("a refused apply started the session: %v", err)
	}

	code, out = runWithStdin(t, s, "", "resource", "apply", "demo-dev", "--ack")
	if code != 0 || !strings.HasPrefix(out, `resource "demo-dev" applied successfully`) {
		t.Fatalf("cerberus resource apply demo-dev --ack: %d %s\n%s", code, out, log)
	}
	var pid int
	for deadline := time.Now().Add(20 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil { //nolint:gosec // the sandbox's own tree
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
	}
	if pid == 0 || syscall.Kill(pid, 0) != nil {
		t.Fatalf("the dev session did not start (pid %d):\n%s", pid, log)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if code, out = runWithStdin(t, s, "", "resource", "status", "demo-dev"); code != 0 || !strings.Contains(out, "Status:      running") {
		t.Fatalf("status after apply: %d %s", code, out)
	}
	if code, out = runWithStdin(t, s, "", "resource", "stop", "demo-dev", "--ack"); code != 0 {
		t.Fatalf("stop: %d %s", code, out)
	}
	for deadline := time.Now().Add(10 * time.Second); syscall.Kill(pid, 0) == nil && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("stop left the session running:\n%s", log)
	}
	stopServe(t, cmd, log)
	calls, _ := os.ReadFile(filepath.Join(s.bin, "calls.log")) //nolint:gosec // the sandbox's own tree
	if strings.Contains(string(calls), " start ") {
		t.Fatalf("systemctl was asked to start something:\n%s", calls)
	}
}
