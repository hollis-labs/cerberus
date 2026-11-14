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
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"
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
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil { //nolint:gosec // fixed arguments
		fmt.Fprintf(os.Stderr, "build cerberus: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// sandbox is a private HOME and XDG tree, a data directory and an override file that
// listens on an ephemeral loopback port.
type sandbox struct {
	home, data, override string
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
		"PATH=" + os.Getenv("PATH"),
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

// start runs `cerberus serve` and returns its base URL once it listens.
func start(t *testing.T, s sandbox) (*exec.Cmd, string, *strings.Builder) {
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
	log := &strings.Builder{}
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
	} {
		t.Run(name, func(t *testing.T) {
			code, out := runToExit(t, newSandbox(t, c.override), "serve")
			if code != 1 || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d, want 1 naming %q:\n%s", code, c.want, out)
			}
		})
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
