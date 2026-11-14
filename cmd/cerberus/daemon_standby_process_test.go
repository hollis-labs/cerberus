package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite" // read the daemon's main.db in the test

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/standby"
)

const childStandbyEnv = "CERBERUS_TEST_STANDBY_DAEMON"

// runChildStandbyDaemon is this test binary acting as `cerberus daemon
// --foreground` under a scratch HOME, with an in-memory credential store
// (so the audit anchor goes to a fake keyring, never the operator's).
func runChildStandbyDaemon() {
	secrets.MockStoreForTests()
	inProcessStateCheck = func() error { return nil }
	rootCmd.SetArgs([]string{"daemon", "--foreground", "--override-supervisor"})
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "child daemon:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// fingerprint is every regular file under root that the check covers:
// mode, size, content hash and mtime.
func fingerprint(t *testing.T, root string, skip func(rel string) bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel != "." && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // the test's own scratch tree
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[rel] = fmt.Sprintf("%v %d %s %d", info.Mode(), info.Size(), hex.EncodeToString(sum[:8]), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// logicalState is main.db's content as data: every table's row count and
// highest rowid, and a digest of the host tables' rows in a total order.
func logicalState(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(n, "sqlite_") {
			tables = append(tables, n)
		}
	}
	_ = rows.Close()
	var b strings.Builder
	for _, tbl := range tables {
		var count int64
		if err := db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %q`, tbl)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		// A WITHOUT ROWID table has no rowid to report.
		maxRowid := "-"
		var m sql.NullInt64
		if err := db.QueryRow(fmt.Sprintf(`SELECT max(rowid) FROM %q`, tbl)).Scan(&m); err == nil {
			maxRowid = fmt.Sprint(m.Int64)
		}
		fmt.Fprintf(&b, "%s %d %s\n", tbl, count, maxRowid)
		if !strings.HasPrefix(tbl, "host_") {
			continue
		}
		probe, err := db.Query(fmt.Sprintf(`SELECT * FROM %q LIMIT 0`, tbl))
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := probe.Columns()
		_ = probe.Close()
		order := make([]string, len(cols))
		for i := range cols {
			order[i] = fmt.Sprint(i + 1)
		}
		dump, err := db.Query(fmt.Sprintf(`SELECT * FROM %q ORDER BY %s`, tbl, strings.Join(order, ",")))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for dump.Next() {
			if err := dump.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			_, _ = fmt.Fprintf(h, "%v\n", vals)
		}
		_ = dump.Close()
		fmt.Fprintf(&b, "  %s rows %x\n", tbl, h.Sum(nil)[:8])
	}
	return b.String()
}

// The live daemon, end to end: entering standby stops the dev sessions
// without pausing them and lists them, stops the resource monitor, and leaves
// the main audit chain, approvals, main.db's logical state and every other
// file under ~/.cerberus as they were, across the flagged resource's start,
// reload and stop and refused writes. Leaving standby starts the listed
// sessions and the monitor again.
func TestDaemonStandbyEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a daemon")
	}
	home, mkErr := os.MkdirTemp("/tmp", "cerbsb-") // short: the socket path is limited to 108 bytes
	if mkErr != nil {
		t.Fatal(mkErr)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	state := filepath.Join(home, ".cerberus")
	work := filepath.Join(home, "work")
	for _, d := range []string{state, work, filepath.Join(state, "approvals")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fmt.Sprintf(`version: 2
resources:
  - id: flagged
    type: process
    connector: local
    env: dev
    owner: self
    config:
      mode: dev_session
      command: ["/bin/sleep", "3600"]
      dir: %[1]s
  - id: other
    type: process
    connector: local
    env: dev
    owner: self
    config:
      mode: dev_session
      command: ["/bin/sleep", "3601"]
      dir: %[1]s
      auto_restart: true
`, work)
	if err := os.WriteFile(filepath.Join(state, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "approvals", "events.jsonl"), []byte(`{"seq":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	xdgData := filepath.Join(home, "xdg-data")
	mainDB := filepath.Join(xdgData, "cerberus", "workspaces", "default", "main.db")

	cmd := exec.Command(os.Args[0], "-test.run=^$") //nolint:gosec // re-executes this test binary as the daemon
	cmd.Env = append(os.Environ(), childStandbyEnv+"=1", "HOME="+home, "TMPDIR="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "xdg-config"), "XDG_DATA_HOME="+xdgData,
		"XDG_CACHE_HOME="+filepath.Join(home, "xdg-cache"), "XDG_STATE_HOME="+filepath.Join(home, "xdg-state"),
		"XPC_SERVICE_NAME=")
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		// The dev sessions are children of the daemon here; stop any left.
		for _, pidFile := range []string{"flagged.pid", "other.pid"} {
			if b, err := os.ReadFile(filepath.Join(state, "pids", pidFile)); err == nil { //nolint:gosec // scratch tree
				var pid int
				if _, err := fmt.Sscan(string(b), &pid); err == nil && pid > 0 {
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
			}
		}
	})

	client := cerbapi.NewSocketClient(filepath.Join(state, "cerberus.sock"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !cond() {
			select {
			case err := <-exited:
				t.Fatalf("the daemon exited (%v) waiting for %s:\n%s", err, what, out.String())
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s:\n%s", what, out.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	running := func(id string) bool {
		st, err := client.GetResourceRuntime(ctx, id)
		return err == nil && st != nil && st.Status == "running"
	}
	logCount := func(event string) int {
		b, _ := os.ReadFile(filepath.Join(state, "cerberus.log")) //nolint:gosec // scratch tree
		return strings.Count(string(b), event)
	}
	waitFor("the socket", func() bool { _, err := client.PingStatus(ctx); return err == nil })

	// Normal operation: both sessions run (the monitor brings up the
	// auto_restart one), and main.db holds some host history.
	if res, err := client.ApplyResource(ctx, "flagged", cerbapi.WithAcknowledged(true)); err != nil || !res.Success {
		t.Fatalf("apply flagged: %+v %v", res, err)
	}
	waitFor("other started by the monitor", func() bool { return running("other") })
	waitFor("the monitor", func() bool { return logCount("daemon.resource_monitor.start") == 1 })
	seed, seedErr := sql.Open("sqlite", mainDB)
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	for i := range 3 {
		if _, err := seed.Exec(`INSERT INTO host_samples (host, ts, load1) VALUES ('box', ?, 0.5)`, 1_700_000_000+i); err != nil {
			t.Fatalf("seed main.db: %v", err)
		}
	}
	_ = seed.Close()

	// Entry.
	paths := standby.PathsIn(state)
	if err := os.WriteFile(paths.Flag, []byte("flagged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor("standby", func() bool {
		st, err := client.PingStatus(ctx)
		return err == nil && st.Standby != nil && st.Standby.Resource == "flagged"
	})
	waitFor("the session list", func() bool { _, ok, _ := paths.ReadSessions(); return ok })
	waitFor("the monitor to stop", func() bool { return logCount("daemon.resource_monitor.stop") == 1 })
	list, _, _ := paths.ReadSessions()
	if strings.Join(list.IDs, ",") != "flagged,other" {
		t.Fatalf("sessions listed = %v", list.IDs)
	}
	for _, id := range []string{"flagged", "other"} {
		if running(id) {
			t.Fatalf("%s still running in standby", id)
		}
		if _, err := os.Stat(filepath.Join(state, "paused."+id)); err == nil {
			t.Fatalf("standby wrote paused.%s", id)
		}
	}

	// Covered: everything under ~/.cerberus except standby's own directory,
	// the daemon's own runtime files (lock, pid, socket, diagnostic logs) and
	// the flagged resource's session files, which starting and stopping it
	// necessarily write.
	skip := func(rel string) bool {
		for _, p := range []string{standby.DirName, "cerberus.log", "logs", "daemon.lock", "cerberus.pid"} {
			if rel == p || strings.HasPrefix(rel, p+string(filepath.Separator)) {
				return true
			}
		}
		return strings.HasPrefix(rel, filepath.Join("pids", "flagged.")) || strings.HasPrefix(rel, filepath.Join("locks", "flagged."))
	}
	before := fingerprint(t, state, skip)
	dbBefore := logicalState(t, mainDB)

	for _, step := range []struct {
		name string
		run  func() (*cerbapi.OpResult, error)
	}{
		{"start", func() (*cerbapi.OpResult, error) {
			return client.ApplyResource(ctx, "flagged", cerbapi.WithAcknowledged(true))
		}},
		{"reload", func() (*cerbapi.OpResult, error) {
			return client.ReloadResource(ctx, "flagged", cerbapi.WithAcknowledged(true))
		}},
		{"stop", func() (*cerbapi.OpResult, error) {
			return client.StopResource(ctx, "flagged", cerbapi.WithAcknowledged(true))
		}},
		{"start again", func() (*cerbapi.OpResult, error) {
			return client.ApplyResource(ctx, "flagged", cerbapi.WithAcknowledged(true))
		}},
	} {
		if res, err := step.run(); err != nil || !res.Success {
			t.Fatalf("%s of the flagged resource in standby: %+v %v", step.name, res, err)
		}
	}
	if _, err := client.ApplyResource(ctx, "other", cerbapi.WithAcknowledged(true)); err == nil || !strings.Contains(err.Error(), "is in standby") {
		t.Fatalf("apply of another resource in standby: %v", err)
	}
	if _, err := client.ListResources(ctx, cerbapi.ResourceListArgs{}); err != nil {
		t.Fatalf("a read in standby: %v", err)
	}
	time.Sleep(2 * time.Second) // a stopped monitor would have had time to act

	if d := diffFingerprints(before, fingerprint(t, state, skip)); len(d) > 0 {
		t.Fatalf("standby wrote under ~/.cerberus:\n%s", strings.Join(d, "\n"))
	}
	if after := logicalState(t, mainDB); after != dbBefore {
		t.Fatalf("main.db changed in standby:\nbefore:\n%s\nafter:\n%s", dbBefore, after)
	}
	if running("other") {
		t.Fatal("the resource monitor restarted other in standby")
	}

	// Exit.
	if err := os.Remove(paths.Flag); err != nil {
		t.Fatal(err)
	}
	waitFor("standby to end", func() bool { st, err := client.PingStatus(ctx); return err == nil && st.Standby == nil })
	waitFor("the listed sessions", func() bool { return running("other") && running("flagged") })
	waitFor("the list to clear", func() bool { _, ok, _ := paths.ReadSessions(); return !ok })
	waitFor("the monitor to start again", func() bool { return logCount("daemon.resource_monitor.start") == 2 })
	if res, err := client.StopResource(ctx, "other", cerbapi.WithAcknowledged(true)); err != nil || !res.Success {
		t.Fatalf("a write after standby: %+v %v", res, err)
	}
}

func diffFingerprints(before, after map[string]string) []string {
	var d []string
	for k, v := range before {
		if after[k] != v {
			d = append(d, fmt.Sprintf("changed or removed: %s (%s -> %s)", k, v, after[k]))
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			d = append(d, "added: "+k)
		}
	}
	return d
}
