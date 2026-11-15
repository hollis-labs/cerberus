package manifest_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/legacyimport/legacyimporttest"
	"github.com/hollis-labs/cerberus/internal/legacyimport/manifest"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

func TestMain(m *testing.M) {
	secrets.MockStoreForTests()
	testhome.Main(m)
}

// live is a "live" old tree: the fixture's tree, the anchor under its own audit dir (as the old
// build keeps it), and main.db in WAL mode with a writer holding it open, as old does.
type live struct {
	home, db string
	writer   *sql.DB
}

func newLive(t *testing.T) live {
	t.Helper()
	f := legacyimporttest.Materialize(t, t.TempDir())
	l := live{home: f.OldHome, db: f.OldDB}
	raw, err := secrets.KeyringGet("cerberus-audit-anchor", f.Account)
	if err != nil {
		t.Fatal(err)
	}
	if err = secrets.KeyringSet("cerberus-audit-anchor", filepath.Join(l.home, "audit"), raw); err != nil {
		t.Fatal(err)
	}
	if l.writer, err = sql.Open("sqlite", l.db); err != nil {
		t.Fatal(err)
	}
	l.writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = l.writer.Close() })
	l.exec(t, "PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0")
	return l
}

func (l live) exec(t *testing.T, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := l.writer.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func (l live) opts() manifest.Options {
	return manifest.Options{OldHome: l.home, OldDB: l.db, Live: true}
}

func (l live) check(t *testing.T, m *manifest.Manifest) []string {
	t.Helper()
	got, err := manifest.Check(context.Background(), m, l.opts())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func walSize(t *testing.T, db string) int64 {
	t.Helper()
	fi, err := os.Stat(db + "-wal")
	if err != nil {
		return 0
	}
	return fi.Size()
}

// The manifest is written once (0600, never over an existing file), reloads with its body
// digest, and checks clean against the tree it was taken of.
func TestManifestWriteLoadCheck(t *testing.T) {
	l := newLive(t)
	out := filepath.Join(t.TempDir(), "manifest.json")
	m, err := manifest.Write(context.Background(), l.opts(), out)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode %v", fi.Mode())
	}
	if _, again := manifest.Write(context.Background(), l.opts(), out); again == nil {
		t.Fatal("a second write replaced the manifest")
	}
	loaded, sum, err := manifest.Load(out)
	if err != nil || sum == "" {
		t.Fatalf("load: %v", err)
	}
	if loaded.Anchor.Absent || len(loaded.Files) < 10 || loaded.DB.HostDumpSHA256 != m.DB.HostDumpSHA256 || loaded.DB.SQLiteSequence["host_events"] != 5 {
		t.Fatalf("manifest: anchor %+v, %d files, db %+v", loaded.Anchor, len(loaded.Files), loaded.DB)
	}
	for _, rel := range []string{"oauth/tokens.jsonl", "registry.yaml", "standby.d/chain.jsonl", "hosts.yaml", "hosts.history.jsonl", "plugin-connectors.json"} {
		if _, ok := loaded.Files[rel]; ok {
			t.Errorf("%s is in the manifest; it is excluded", rel)
		}
	}
	for rel, want := range map[string]bool{"paused": false, "paused.web": false, "paused.ghost.x": false, "brakes/events.jsonl": false, "audit/.digest_key": false} {
		if got, ok := loaded.Files[rel]; !ok || got.Absent != want {
			t.Errorf("%s: %+v %v", rel, got, ok)
		}
	}
	if got := l.check(t, loaded); len(got) != 0 {
		t.Fatalf("a clean tree mismatches: %v", got)
	}
	// An edited manifest does not load.
	data, _ := os.ReadFile(out) //nolint:gosec // the test's own file
	edited := filepath.Join(t.TempDir(), "edited.json")
	tampered := []byte(strings.Replace(string(data), `"count": 120`, `"count": 121`, 1))
	if err := os.WriteFile(edited, tampered, 0o600); err != nil { //nolint:gosec // the test's own file
		t.Fatal(err)
	}
	if _, _, err := manifest.Load(edited); err == nil || !strings.Contains(err.Error(), "edited") {
		t.Fatalf("an edited manifest loaded: %v", err)
	}
}

// check detects a changed file, a new paused.x, a new receipt and a logical main.db change; it
// ignores a WAL write that changes nothing logically and its checkpoint, and it never
// checkpoints the live database itself.
func TestManifestCheckDetectsChangesAndIgnoresCheckpoints(t *testing.T) {
	l := newLive(t)
	m, err := manifest.Take(context.Background(), l.opts())
	if err != nil {
		t.Fatal(err)
	}
	expect := func(t *testing.T, want string) {
		t.Helper()
		got := l.check(t, m)
		if want == "" && len(got) != 0 {
			t.Fatalf("mismatches %v, want none", got)
		}
		if want != "" && (len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), want)) {
			t.Fatalf("mismatches %v, want one naming %q", got, want)
		}
	}

	// Bytes change, logic does not: a row written and removed through the WAL, then checkpointed.
	l.exec(t, `INSERT INTO host_samples (host, ts, cpu) VALUES ('box-9', 1, 1)`, `DELETE FROM host_samples WHERE host = 'box-9'`)
	if walSize(t, l.db) == 0 {
		t.Fatal("the write did not go through the WAL")
	}
	expect(t, "")
	l.exec(t, "PRAGMA wal_checkpoint(TRUNCATE)")
	expect(t, "")

	// A logical change still in the WAL (un-checkpointed) is seen, and check does not checkpoint it.
	l.exec(t, `UPDATE host_samples SET cpu = cpu + 1 WHERE ts = (SELECT MIN(ts) FROM host_samples)`)
	before := walSize(t, l.db)
	expect(t, "main.db: host rows digest")
	if after := walSize(t, l.db); after != before || after == 0 {
		t.Fatalf("the WAL went from %d to %d bytes across a check: it checkpointed", before, after)
	}
	l.exec(t, `UPDATE host_samples SET cpu = cpu - 1 WHERE ts = (SELECT MIN(ts) FROM host_samples)`)
	expect(t, "")

	// Files.
	audit := filepath.Join(l.home, "audit", "2026-11.jsonl")
	data, _ := os.ReadFile(audit)                                          //nolint:gosec // the test's own file
	if err := os.WriteFile(audit, append(data, '\n'), 0o600); err != nil { //nolint:gosec // the test's own file
		t.Fatal(err)
	}
	expect(t, "audit/2026-11.jsonl")
	if err := os.WriteFile(audit, data, 0o600); err != nil { //nolint:gosec // a path in the test's own dir
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(l.home, "paused.x"), []byte("paused\n"), 0o600); err != nil { //nolint:gosec // a path in the test's own dir
		t.Fatal(err)
	}
	expect(t, "paused.x: present")
	_ = os.Remove(filepath.Join(l.home, "paused.x"))

	if err := os.Remove(filepath.Join(l.home, "paused")); err != nil {
		t.Fatal(err)
	}
	expect(t, "paused: gone")
	_ = os.WriteFile(filepath.Join(l.home, "paused"), []byte("paused\n"), 0o600) //nolint:gosec // a path in the test's own dir

	expect(t, "")

	// The files that stay in place in the adopted root are new's to edit while it runs: a
	// rollback keeps those edits, so the check does not see them.
	for _, rel := range []string{"hosts.yaml", "hosts.history.jsonl", "hosts.history.jsonl.1", "plugin-connectors.json", "registry.yaml"} {
		appendTo(t, filepath.Join(l.home, rel), "\n# edited by the new build\n")
	}
	expect(t, "")

	// Last: an event deleted (the janitor's kind of change) is a mismatch.
	l.exec(t, `DELETE FROM host_events WHERE id = 3`)
	expect(t, "host_events: 2 rows, was 3")
}

// appendTo appends to a file of the test's tree, creating it when absent.
func appendTo(t *testing.T, p, data string) {
	t.Helper()
	fh, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // the test's own tree
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fh.WriteString(data); err != nil {
		t.Fatal(err)
	}
	if err = fh.Close(); err != nil {
		t.Fatal(err)
	}
}

// Expect is the manifest after the overridden steps: a tree in that state compares equal to it,
// the manifest itself is unchanged, and Diff keys each mismatch by what it is.
func TestExpectOverridesWhatAStepWrote(t *testing.T) {
	l := newLive(t)
	ctx := context.Background()
	m, err := manifest.Take(ctx, l.opts())
	if err != nil {
		t.Fatal(err)
	}
	before := m.Files["approvals/events.jsonl"]

	appendTo(t, filepath.Join(l.home, "approvals", "events.jsonl"), "{}\n")
	if werr := os.WriteFile(filepath.Join(l.home, "paused.new"), []byte("paused\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if rerr := os.Remove(filepath.Join(l.home, "paused.web")); rerr != nil {
		t.Fatal(rerr)
	}
	now, err := manifest.Take(ctx, l.opts())
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, d := range manifest.Diff(m, now) {
		keys[d.Key] = true
	}
	for _, k := range []string{"approvals/events.jsonl", "paused.new", "paused.web"} {
		if !keys[k] {
			t.Errorf("Diff has no mismatch keyed %s: %v", k, keys)
		}
	}

	o := manifest.Overrides{Files: map[string]manifest.File{
		"approvals/events.jsonl": now.Files["approvals/events.jsonl"],
		"paused.new":             now.Files["paused.new"],
		"paused.web":             {Absent: true},
	}}
	if got := manifest.Compare(manifest.Expect(m, o), now); len(got) != 0 {
		t.Fatalf("the tree after the steps does not match Expect: %v", got)
	}
	if m.Files["approvals/events.jsonl"] != before || m.Files["paused.new"] != (manifest.File{}) {
		t.Fatal("Expect changed the manifest it cloned")
	}
	// Overriding one file only: the other two still mismatch.
	partial := manifest.Overrides{Files: map[string]manifest.File{"paused.web": {Absent: true}}}
	if got := manifest.Diff(manifest.Expect(m, partial), now); len(got) != 2 {
		t.Fatalf("partial overrides: %v", got)
	}
	// The anchor and the database are overridden whole.
	head := []byte(`{"seq":99,"hash":"sha256:x","file":"2026-11.jsonl","offset":1}`)
	moved := manifest.Expect(m, manifest.Overrides{Anchor: &manifest.Anchor{Account: m.Anchor.Account, Head: head}})
	if d := manifest.Diff(moved, m); len(d) != 1 || d[0].Key != manifest.KeyAnchor {
		t.Fatalf("anchor override: %v", d)
	}
}

// The kept manifest loads from the data directory, with its body digest checked; a data
// directory without one is an error that says so.
func TestLoadKept(t *testing.T) {
	l := newLive(t)
	data := t.TempDir()
	if _, _, err := manifest.LoadKept(data); err == nil || !strings.Contains(err.Error(), "kept no D3 manifest") {
		t.Fatalf("no kept manifest: %v", err)
	}
	kept := filepath.Join(data, filepath.FromSlash(manifest.KeptRel))
	if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Write(context.Background(), l.opts(), kept)
	if err != nil {
		t.Fatal(err)
	}
	got, sum, err := manifest.LoadKept(data)
	if err != nil || got.BodySHA256 != m.BodySHA256 || !strings.HasPrefix(sum, "sha256:") {
		t.Fatalf("LoadKept: %v %v", got, err)
	}
}
