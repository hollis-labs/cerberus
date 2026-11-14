package datadir

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func xdg(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	return base
}

func TestDefaultIsSeparateFromTheOldBuildAndCreatesNothing(t *testing.T) {
	base := xdg(t)
	d, err := Resolve(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "data", "cerberus-substrate"); d.Data != want {
		t.Fatalf("data = %q, want %q", d.Data, want)
	}
	for _, dir := range []string{"data", "config", "state", "cache"} {
		if _, err := os.Stat(filepath.Join(base, dir)); err == nil {
			t.Fatalf("Resolve created %s", dir)
		}
	}
}

func TestExplicitDirectoryWinsAndIsMadeAbsolute(t *testing.T) {
	xdg(t)
	t.Chdir(t.TempDir())
	d, err := Resolve(env(map[string]string{Env: "rel"}))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(d.Data) || filepath.Base(d.Data) != "rel" {
		t.Fatalf("data = %q", d.Data)
	}
	if d.Database() != filepath.Join(d.Data, "cerberus.db") {
		t.Fatalf("database = %q", d.Database())
	}
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestPrepareCreatesAnOwnerOnlyDirectory(t *testing.T) {
	d := Dirs{Data: filepath.Join(t.TempDir(), "a", "b")}
	if err := d.Prepare(); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, d.Data); m != 0o700 {
		t.Fatalf("mode = %v, want 0700", m)
	}
	if err := d.Prepare(); err != nil { // an existing private directory is fine
		t.Fatal(err)
	}
}

func TestPrepareNeverChangesAnExistingDirectory(t *testing.T) {
	d := Dirs{Data: t.TempDir()}
	if err := os.Chmod(d.Data, 0o755); err != nil { //nolint:gosec // loose on purpose: Prepare must refuse it, not tighten it
		t.Fatal(err)
	}
	err := d.Prepare()
	if err == nil || !strings.Contains(err.Error(), "chmod 700") {
		t.Fatalf("err = %v, want a refusal naming the fix", err)
	}
	if m := mode(t, d.Data); m != 0o755 {
		t.Fatalf("Prepare changed the mode to %v", m)
	}
}

func TestAnchor(t *testing.T) {
	d := Dirs{Data: "/data"}
	for _, c := range []struct{ in, want string }{
		{"", "/data/cerberus.db"},
		{"x.db", "/data/x.db"},
		{"sub/x.db", "/data/sub/x.db"},
		{"/elsewhere/x.db", "/elsewhere/x.db"},
	} {
		got, err := d.Anchor(d.Database(), c.in)
		if err != nil || got != c.want {
			t.Fatalf("Anchor(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := d.Anchor(d.Database(), "../x.db"); err == nil {
		t.Fatal("a relative path that leaves the data directory was accepted")
	}
}

func TestPrivateFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "new")
	if err := PrivateFile(p); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, p); m != 0o600 {
		t.Fatalf("mode = %v, want 0600", m)
	}
	loose := filepath.Join(dir, "loose")
	if err := os.WriteFile(loose, []byte("x"), 0o644); err != nil { //nolint:gosec // loose on purpose: PrivateFile must refuse it, not tighten it
		t.Fatal(err)
	}
	if err := PrivateFile(loose); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v, want a refusal naming the fix", err)
	}
	if m := mode(t, loose); m != 0o644 {
		t.Fatalf("PrivateFile changed the mode to %v", m)
	}
}

// fixture is a home with both of the old build's homes and a fresh data directory.
type fixture struct {
	home, oldState, oldXDG string
	roots                  []string
	d                      Dirs
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := xdg(t)
	f := fixture{home: filepath.Join(base, "home")}
	f.oldState = filepath.Join(f.home, ".cerberus")
	f.oldXDG = filepath.Join(base, "data", "cerberus")
	for _, dir := range []string{filepath.Join(f.oldState, "apps"), filepath.Join(f.oldXDG, "workspaces", "default")} {
		if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the old build's tree is not private on every host
			t.Fatal(err)
		}
	}
	var err error
	if f.roots, err = OldRoots(f.home); err != nil {
		t.Fatal(err)
	}
	f.d = Dirs{Data: filepath.Join(base, "data", "cerberus-substrate")}
	return f
}

func wantLegacy(t *testing.T, err error, mention string) {
	t.Helper()
	if !errors.Is(err, ErrLegacyLayout) {
		t.Fatalf("err = %v, want ErrLegacyLayout", err)
	}
	if !strings.Contains(err.Error(), mention) || !strings.Contains(err.Error(), Env) {
		t.Fatalf("err = %q, want it to name %q and say what to do (%s)", err, mention, Env)
	}
}

func TestOldRootsAreBothHomes(t *testing.T) {
	f := newFixture(t)
	if len(f.roots) != 2 || f.roots[0] != f.oldState || f.roots[1] != f.oldXDG {
		t.Fatalf("roots = %q, want [%s %s]", f.roots, f.oldState, f.oldXDG)
	}
}

func TestGuardAcceptsANewDataDirectory(t *testing.T) {
	f := newFixture(t)
	if err := RefuseOldPaths(f.d, f.d.Database(), f.roots); err != nil {
		t.Fatal(err)
	}
	if err := RefuseOldContents(f.d); err != nil { // missing directory
		t.Fatal(err)
	}
	if err := f.d.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err := RefuseOldContents(f.d); err != nil { // empty directory
		t.Fatal(err)
	}
	if err := RefuseOldSchema(f.d.Database()); err != nil { // missing file
		t.Fatal(err)
	}
	if err := PrivateFile(f.d.Database()); err != nil {
		t.Fatal(err)
	}
	if err := RefuseOldSchema(f.d.Database()); err != nil { // zero-byte file
		t.Fatal(err)
	}
}

func TestGuardRefusesPathsInEitherOldHome(t *testing.T) {
	f := newFixture(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(f.oldXDG, link); err != nil {
		t.Fatal(err)
	}
	stateLink := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(f.oldState, stateLink); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		d       Dirs
		db, why string
	}{
		"data dir is the state tree":      {Dirs{Data: f.oldState}, "", ".cerberus"},
		"data dir inside the state tree":  {Dirs{Data: filepath.Join(f.oldState, "apps")}, "", ".cerberus"},
		"data dir via a link to the tree": {Dirs{Data: filepath.Join(stateLink, "apps")}, "", ".cerberus"},
		"database in the state tree":      {f.d, filepath.Join(f.oldState, "cerberus.db"), "database.sqlite.path"},
		"data dir is the old XDG dir":     {Dirs{Data: f.oldXDG}, "", "the data directory"},
		"data dir inside the old XDG dir": {Dirs{Data: filepath.Join(f.oldXDG, "workspaces", "default")}, "", "the data directory"},
		"data dir via a link to old XDG":  {Dirs{Data: filepath.Join(link, "workspaces", "default")}, "", "the data directory"},
		"database in the old XDG dir":     {f.d, filepath.Join(f.oldXDG, "workspaces", "default", "main.db"), "database.sqlite.path"},
		"database via a link to old XDG":  {f.d, filepath.Join(link, "workspaces", "default", "main.db"), "database.sqlite.path"},
	} {
		t.Run(name, func(t *testing.T) {
			db := c.db
			if db == "" {
				db = c.d.Database()
			}
			wantLegacy(t, RefuseOldPaths(c.d, db, f.roots), c.why)
		})
	}
}

func TestGuardRefusesTheOldLayoutInANewDirectory(t *testing.T) {
	f := newFixture(t)
	if err := f.d.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.d.Data, "workspaces", "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	wantLegacy(t, RefuseOldContents(f.d), "workspaces/")
}

func TestGuardRefusesEntriesOfTheOldStateTree(t *testing.T) {
	for _, m := range oldStateMarkers {
		t.Run(m, func(t *testing.T) {
			f := newFixture(t)
			if err := f.d.Prepare(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.d.Data, m), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			wantLegacy(t, RefuseOldContents(f.d), m)
		})
	}
}

// walDatabase makes a WAL database whose main file holds tables and whose WAL holds frames
// not yet checkpointed, and keeps a connection open so -wal and -shm stay, like a live
// database another process is using. The connection is closed at cleanup.
func walDatabase(t *testing.T, path, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE " + table + " (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)",
		"PRAGMA wal_checkpoint(TRUNCATE)", // the table reaches the main file
		"INSERT INTO " + table + " VALUES (8, '2026-10-30T18:46:47Z')",
		"INSERT INTO " + table + " VALUES (9, '2026-10-31T00:00:00Z')", // pending frames
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if fi, err := os.Stat(path + suffix); err != nil || fi.Size() == 0 {
			t.Fatalf("fixture has no %s with content: %v", suffix, err)
		}
	}
}

// snapshot reads the database and its -wal and -shm.
func snapshot(t *testing.T, path string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(path + suffix) //nolint:gosec // the test's own temp file
		if err != nil {
			t.Fatal(err)
		}
		out[suffix] = b
	}
	return out
}

func TestGuardRefusesTheOldSchemaWithoutTouchingAnyFile(t *testing.T) {
	old := filepath.Join(t.TempDir(), "main.db")
	walDatabase(t, old, "schema_migrations")
	before := snapshot(t, old)

	wantLegacy(t, RefuseOldSchema(old), "old build's schema")

	after := snapshot(t, old)
	for suffix, b := range before {
		if !bytes.Equal(b, after[suffix]) {
			t.Fatalf("the guard changed main.db%s (%d bytes -> %d)", suffix, len(b), len(after[suffix]))
		}
	}
}

func TestGuardAcceptsANewDatabaseWithoutTouchingAnyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cerberus.db")
	walDatabase(t, p, "goose_db_version")
	before := snapshot(t, p)
	if err := RefuseOldSchema(p); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, p)
	for suffix, b := range before {
		if !bytes.Equal(b, after[suffix]) {
			t.Fatalf("the guard changed cerberus.db%s", suffix)
		}
	}
}

func TestGuardRefusesAFileItCannotRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(p, bytes.Repeat([]byte("not sqlite "), 100), 0o600); err != nil {
		t.Fatal(err)
	}
	err := RefuseOldSchema(p)
	if err == nil || !strings.Contains(err.Error(), "without writing") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}
