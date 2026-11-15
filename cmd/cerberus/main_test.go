package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hollis-labs/cerberus/internal/testhome"
)

// binary is the real cerberus binary, built once for the tests that run it as a process.
var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cerberus-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "cerberus")
	if out, buildErr := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); buildErr != nil { //nolint:gosec // fixed arguments
		fmt.Fprintf(os.Stderr, "build cerberus: %v\n%s", buildErr, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	// The binary is built first, with the toolchain's own caches; the tests then run under
	// a scratch home (internal/testhome), and each child they start gets a sandbox of its own.
	cleanupHome, err := testhome.Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	cleanupHome()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// sandbox is a private HOME and XDG tree, a data directory and an override file that
// listens on an ephemeral loopback port.
type sandbox struct {
	home, data, override string
	// bin, when set, comes first on PATH: a test's fake systemctl, so the binary never reaches
	// the host's user manager.
	bin string
}

func newSandbox(t *testing.T, override string) sandbox {
	t.Helper()
	base := t.TempDir()
	s := sandbox{home: filepath.Join(base, "home"), data: filepath.Join(base, "data"), override: filepath.Join(base, "override.yaml")}
	if err := os.MkdirAll(s.home, 0o700); err != nil {
		t.Fatal(err)
	}
	if override == "" {
		override = "httphost: { addr: \"127.0.0.1:0\" }\n"
	}
	if err := os.WriteFile(s.override, []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s sandbox) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // the test binary
	cmd.Env = []string{
		"HOME=" + s.home,
		"PATH=" + strings.TrimPrefix(s.bin+":"+os.Getenv("PATH"), ":"),
		"XDG_DATA_HOME=" + filepath.Join(s.home, ".local", "share"),
		"XDG_CONFIG_HOME=" + filepath.Join(s.home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(s.home, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(s.home, ".cache"),
		"CERBERUS_DATA_DIR=" + s.data,
		"SUBSTRATE_CONFIG=" + s.override,
	}
	return cmd
}

var listening = regexp.MustCompile(`msg="cerberus listening".*addr=(\S+)`)

// serveLog is serve's stderr as the reader goroutine collects it; the test reads it while the
// process still logs, so it is guarded.
type serveLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *serveLog) WriteString(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.WriteString(s)
}

func (l *serveLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// start runs `cerberus serve` and returns its base URL once it listens.
func start(t *testing.T, s sandbox) (*exec.Cmd, string, *serveLog) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	cmd := s.command(ctx, "serve")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	log := &serveLog{}
	addr := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.WriteString(sc.Text() + "\n")
			if m := listening.FindStringSubmatch(sc.Text()); m != nil {
				addr <- m[1]
			}
		}
		close(addr)
	}()
	select {
	case a, ok := <-addr:
		if !ok {
			t.Fatalf("cerberus exited before listening:\n%s", log)
		}
		return cmd, "http://" + a, log
	case <-time.After(30 * time.Second):
		t.Fatalf("cerberus did not listen within 30s:\n%s", log)
	}
	return nil, "", nil
}

func TestServeAnswersHealthAndDrainsOnSIGTERM(t *testing.T) {
	s := newSandbox(t, "")
	cmd, base, log := start(t, s)

	resp, err := http.Get(base + "/healthz") //nolint:noctx // test against the loopback child
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("GET /healthz = %d %s", resp.StatusCode, body)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit after SIGTERM: %v\n%s", err, log)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("cerberus did not exit within 20s of SIGTERM:\n%s", log)
	}

	for p, want := range map[string]os.FileMode{s.data: 0o700, filepath.Join(s.data, "cerberus.db"): 0o600} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Fatalf("%s mode = %v, want %v", p, fi.Mode().Perm(), want)
		}
	}
	// The old build's homes are never created.
	for _, old := range []string{filepath.Join(s.home, ".cerberus"), filepath.Join(s.home, ".local", "share", "cerberus")} {
		if _, err := os.Stat(old); err == nil {
			t.Fatalf("serve created %s", old)
		}
	}
}

// runToExit runs cerberus to completion and returns its exit code and combined output.
func runToExit(t *testing.T, s sandbox, args ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := s.command(ctx, args...).CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &ee):
		return ee.ExitCode(), string(out)
	}
	t.Fatal(err)
	return 0, ""
}

func TestServeRefusesTheOldBuildsData(t *testing.T) {
	t.Run("old XDG layout", func(t *testing.T) {
		s := newSandbox(t, "")
		if err := os.MkdirAll(filepath.Join(s.data, "workspaces", "default"), 0o700); err != nil {
			t.Fatal(err)
		}
		code, out := runToExit(t, s, "serve")
		if code != 1 || !strings.Contains(out, "old Cerberus build") || !strings.Contains(out, "CERBERUS_DATA_DIR") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if _, err := os.Stat(filepath.Join(s.data, "cerberus.db")); err == nil {
			t.Fatal("a database was created in a refused directory")
		}
	})
	t.Run("old state tree", func(t *testing.T) {
		s := newSandbox(t, "")
		s.data = filepath.Join(s.home, ".cerberus")
		if err := os.MkdirAll(s.data, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out := runToExit(t, s, "serve")
		if code != 1 || !strings.Contains(out, ".cerberus") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("a directory in the old state tree keeps its mode", func(t *testing.T) {
		s := newSandbox(t, "")
		s.data = filepath.Join(s.home, ".cerberus", "apps")
		if err := os.MkdirAll(s.data, 0o755); err != nil { //nolint:gosec // the old tree's mode, which must survive
			t.Fatal(err)
		}
		if err := os.Chmod(s.data, 0o755); err != nil { //nolint:gosec // the old tree's mode, which must survive
			t.Fatal(err)
		}
		code, out := runToExit(t, s, "serve")
		if code != 1 || !strings.Contains(out, ".cerberus") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		fi, err := os.Stat(s.data)
		if err != nil || fi.Mode().Perm() != 0o755 {
			t.Fatalf("a refused start changed %s: %v %v", s.data, fi.Mode().Perm(), err)
		}
	})
	t.Run("old XDG data directory", func(t *testing.T) {
		s := newSandbox(t, "")
		old := filepath.Join(s.home, ".local", "share", "cerberus", "workspaces", "default")
		if err := os.MkdirAll(old, 0o700); err != nil {
			t.Fatal(err)
		}
		s.data = old
		code, out := runToExit(t, s, "serve")
		if code != 1 || !strings.Contains(out, "old Cerberus build") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		entries, _ := os.ReadDir(old)
		if len(entries) != 0 {
			t.Fatalf("a refused start created %d entries beside the old database", len(entries))
		}
	})
	t.Run("old database named by the override", func(t *testing.T) {
		old := "database: { sqlite: { path: \"%s\" } }\nhttphost: { addr: \"127.0.0.1:0\" }\n"
		s := newSandbox(t, "")
		db := filepath.Join(s.home, ".local", "share", "cerberus", "workspaces", "default", "main.db")
		if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(db, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.override, []byte(fmt.Sprintf(old, db)), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out := runToExit(t, s, "serve")
		if code != 1 || !strings.Contains(out, "database.sqlite.path") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if b, _ := os.ReadFile(db); string(b) != "old" { //nolint:gosec // the test's own temp file
			t.Fatal("a refused start changed the old database")
		}
		if _, err := os.Stat(s.data); err == nil {
			t.Fatal("a refused start created the data directory")
		}
	})
}

func TestServeRefusesBadConfig(t *testing.T) {
	for name, c := range map[string]struct{ override, want string }{
		"non-loopback":  {"httphost: { addr: \"0.0.0.0:0\" }\n", "loopback httphost.addr"},
		"unknown key":   {"httphost: { addr: \"127.0.0.1:0\" }\ncerberus: { install_rot: /x }\n", "unknown config key(s): install_rot"},
		"relative root": {"httphost: { addr: \"127.0.0.1:0\" }\ncerberus: { install_root: apps }\n", "cerberus.install_root"},
		"postgres":      {"httphost: { addr: \"127.0.0.1:0\" }\ndatabase: { driver: postgres }\n", "SQLite only"},
		"bad mode":      {"httphost: { addr: \"127.0.0.1:0\" }\ncerberus: { mode: chaos }\n", "cerberus.mode must be observe or supervise"},
		"unknown self":  {"httphost: { addr: \"127.0.0.1:0\" }\ncerberus: { self_resource_id: ghost }\n", "lists no such resource"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out := runToExit(t, newSandbox(t, c.override), "serve")
			if code != 1 || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d, want 1 naming %q:\n%s", code, c.want, out)
			}
		})
	}
}

// Supervise mode refuses to start while a policy is configured: until slice 6 nothing would
// enforce it.
func TestServeInSuperviseModeRefusesAConfiguredPolicy(t *testing.T) {
	s := newSandbox(t, "httphost: { addr: \"127.0.0.1:0\" }\ncerberus: { mode: supervise }\n")
	dir := filepath.Join(s.home, ".config", "cerberus", "policy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "applied.yaml"), []byte("rules: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := runToExit(t, s, "serve")
	if code != 1 || !strings.Contains(out, "a policy is configured") || !strings.Contains(out, "slice 6") {
		t.Fatalf("exit %d, want 1 naming the policy:\n%s", code, out)
	}
}

// stopServe sends SIGTERM and waits for a clean exit.
func stopServe(t *testing.T, cmd *exec.Cmd, log fmt.Stringer) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit after SIGTERM: %v\n%s", err, log)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("cerberus did not exit within 20s of SIGTERM:\n%s", log)
	}
}

// schemaOf lists the database's tables and the goose rows, read-only.
func schemaOf(t *testing.T, path string) (tables string, versions int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// The binary is SQLite only; this reads its file directly.
	// gostore:allow sqlite-only like-case
	if err := db.QueryRow(`SELECT group_concat(name, ',') FROM (SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name)`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	return tables, versions
}

func TestServeMigratesTheSchemaOnceAcrossRestarts(t *testing.T) {
	s := newSandbox(t, "")
	dbPath := filepath.Join(s.data, "cerberus.db")

	cmd, _, log := start(t, s)
	stopServe(t, cmd, log)
	tables, versions := schemaOf(t, dbPath)
	for _, want := range []string{"host_samples", "host_events", "pause_state", "applied_definitions", "credential_issuance_receipts"} {
		if !strings.Contains(","+tables+",", ","+want+",") {
			t.Fatalf("after the first start the database lacks %s: %s", want, tables)
		}
	}

	cmd, _, log = start(t, s)
	stopServe(t, cmd, log)
	if t2, v2 := schemaOf(t, dbPath); t2 != tables || v2 != versions {
		t.Fatalf("second start changed the schema: %s (%d goose rows), was %s (%d)", t2, v2, tables, versions)
	}
}

// Observe mode, started over a populated old state tree, writes nothing in it: the registry,
// a descriptor it lists, a stale dev session pid file and the unit directory are as they
// were after a serve and a SIGTERM.
func TestServeInObserveModeWritesNothingInTheAdoptedTree(t *testing.T) {
	s := newSandbox(t, "")
	state := filepath.Join(s.home, ".cerberus")
	desc := filepath.Join(s.home, "apps", "demo.cerberus.yaml")
	for _, d := range []string{filepath.Join(state, "pids"), filepath.Dir(desc), filepath.Join(s.home, ".config", "systemd", "user")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		desc: "kind: cerberus-project/v1\nowner: demo\nnamespace: local\nproject:\n  id: demo\n  name: Demo\nresources:\n" +
			"  - id: demo-dev\n    type: process\n    project: demo\n    connector: local\n    env: dev\n    owner: self\n    admin: self\n    config:\n      mode: dev_session\n      command: [\"/bin/sleep\", \"60\"]\n",
		filepath.Join(state, "registry.yaml"):                            "version: 1\nentries:\n  - owner: demo\n    namespace: local\n    path: " + desc + "\n    kind: cerberus-project/v1\n",
		filepath.Join(state, "pids", "demo-dev.pid"):                     "999999\n",
		filepath.Join(s.home, ".config", "systemd", "user", "x.service"): "[Unit]\n",
	}
	for p, content := range files {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snap := func() map[string]string {
		out := map[string]string{}
		for _, root := range []string{state, filepath.Join(s.home, "apps"), filepath.Join(s.home, ".config")} {
			_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
				if err == nil {
					b, _ := os.ReadFile(p) //nolint:gosec // the sandbox's own tree
					out[p] = fmt.Sprintf("%v %d %d %x", info.Mode(), info.Size(), info.ModTime().UnixNano(), b)
				}
				return nil
			})
		}
		return out
	}
	before := snap()
	cmd, _, log := start(t, s)
	if !strings.Contains(log.String(), "cerberus supervision") || !strings.Contains(log.String(), "mode=observe") || !strings.Contains(log.String(), "resources=1") {
		t.Fatalf("the supervision lane did not start in observe mode over the registry:\n%s", log)
	}
	stopServe(t, cmd, log)
	after := snap()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("changed: %s", k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("added: %s", k)
		}
	}
}

// fakeSystemctl is a systemctl on PATH that records every call and reports every unit loaded,
// enabled and running.
const fakeSystemctl = `#!/bin/sh
echo "$*" >> "$(dirname "$0")/calls.log"
case "$2" in
show) printf 'Id=%s\nLoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled\nNeedDaemonReload=no\nMainPID=4242\n' "$3" ;;
esac
exit 0
`

// Supervise mode's stop order (plan section 4.4), with a real process and SIGTERM: HTTP stops
// first, then the resource monitor; the process exits 0; no os_service unit is stopped,
// disabled, killed or restarted, and the dev session the monitor started is left running.
func TestServeInSuperviseModeStopsInOrderAndLeavesWorkloads(t *testing.T) {
	s := newSandbox(t, "httphost: { addr: \"127.0.0.1:0\" }\ncerberus: { mode: supervise }\n")
	s.bin = filepath.Join(filepath.Dir(s.home), "bin")
	if err := os.MkdirAll(s.bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.bin, "systemctl"), []byte(fakeSystemctl), 0o700); err != nil { //nolint:gosec // the fake must run
		t.Fatal(err)
	}
	state := filepath.Join(s.home, ".cerberus")
	desc := filepath.Join(s.home, "apps", "demo.cerberus.yaml")
	if err := os.MkdirAll(filepath.Dir(desc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	descriptor := "kind: cerberus-project/v1\nowner: demo\nnamespace: local\nproject:\n  id: demo\n  name: Demo\nresources:\n" +
		"  - id: demo-svc\n    type: process\n    project: demo\n    connector: local\n    env: dev\n    owner: self\n    admin: self\n    config:\n      mode: os_service\n      supervisor: systemd_user\n      command: [\"/bin/sleep\", \"300\"]\n" +
		"  - id: demo-dev\n    type: process\n    project: demo\n    connector: local\n    env: dev\n    owner: self\n    admin: self\n    config:\n      mode: dev_session\n      auto_restart: true\n      command: [\"/bin/sleep\", \"300\"]\n"
	registry := "version: 1\nentries:\n  - owner: demo\n    namespace: local\n    path: " + desc + "\n    kind: cerberus-project/v1\n"
	for p, content := range map[string]string{desc: descriptor, filepath.Join(state, "registry.yaml"): registry} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd, _, log := start(t, s)
	pidFile := filepath.Join(s.data, "pids", "demo-dev.pid")
	var pid int
	deadline := time.Now().Add(20 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil { //nolint:gosec // the sandbox's own tree
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatalf("the monitor did not start the auto_restart session:\n%s", log)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	stopServe(t, cmd, log)

	out := log.String()
	httpAt, monitorAt := strings.Index(out, "cerberus http stopped"), strings.Index(out, "daemon.resource_monitor.stop")
	if httpAt < 0 || monitorAt < 0 || httpAt > monitorAt {
		t.Fatalf("stop order: http at %d, monitor at %d, want http first:\n%s", httpAt, monitorAt, out)
	}
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("the dev session did not outlive cerberus")
	}
	calls, _ := os.ReadFile(filepath.Join(s.bin, "calls.log")) //nolint:gosec // the sandbox's own tree
	for _, verb := range []string{" stop ", " kill ", " disable ", " restart ", " start "} {
		if strings.Contains(" "+strings.ReplaceAll(string(calls), "\n", " \n ")+" ", verb) {
			t.Fatalf("cerberus ran systemctl%s on exit or start:\n%s", verb, calls)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "pids")); !os.IsNotExist(err) {
		t.Fatalf("the old tree's pids/ was touched: %v", err)
	}
}
