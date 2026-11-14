package datadir

import (
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

func TestPrepareMakesTheDirectoryOwnerOnly(t *testing.T) {
	d := Dirs{Data: filepath.Join(t.TempDir(), "a", "b")}
	if err := d.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d.Data, 0o755); err != nil { //nolint:gosec // loosened on purpose: Prepare must tighten it
		t.Fatal(err)
	}
	if err := d.Prepare(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(d.Data)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700", fi.Mode().Perm())
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

func TestPrivateFileTightensMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, nil, 0o644); err != nil { //nolint:gosec // loose on purpose: PrivateFile must tighten it
		t.Fatal(err)
	}
	if err := PrivateFile(p); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

// guardFixture is a home with an old-build state tree and a fresh data directory.
func guardFixture(t *testing.T) (home string, d Dirs) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	if err := os.MkdirAll(filepath.Join(home, ".cerberus", "audit"), 0o700); err != nil {
		t.Fatal(err)
	}
	d = Dirs{Data: filepath.Join(base, "data")}
	if err := d.Prepare(); err != nil {
		t.Fatal(err)
	}
	return home, d
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

func TestGuardAcceptsAnEmptyDataDirectory(t *testing.T) {
	home, d := guardFixture(t)
	if err := RefuseLegacy(d, d.Database(), home); err != nil {
		t.Fatal(err)
	}
	// A zero-byte database (created 0600 ahead of the open) is new, not old.
	if err := os.WriteFile(d.Database(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RefuseLegacy(d, d.Database(), home); err != nil {
		t.Fatal(err)
	}
}

func TestGuardRefusesTheOldStateTree(t *testing.T) {
	home, _ := guardFixture(t)
	old := Dirs{Data: filepath.Join(home, ".cerberus")}
	wantLegacy(t, RefuseLegacy(old, old.Database(), home), ".cerberus")

	inside := Dirs{Data: filepath.Join(home, ".cerberus", "substrate")}
	wantLegacy(t, RefuseLegacy(inside, inside.Database(), home), ".cerberus")
}

func TestGuardRefusesASymlinkIntoTheOldStateTree(t *testing.T) {
	home, d := guardFixture(t)
	link := filepath.Join(filepath.Dir(d.Data), "link")
	if err := os.Symlink(filepath.Join(home, ".cerberus"), link); err != nil {
		t.Fatal(err)
	}
	ld := Dirs{Data: link}
	wantLegacy(t, RefuseLegacy(ld, ld.Database(), home), ".cerberus")
}

func TestGuardRefusesADatabasePathInTheOldStateTree(t *testing.T) {
	home, d := guardFixture(t)
	wantLegacy(t, RefuseLegacy(d, filepath.Join(home, ".cerberus", "cerberus.db"), home), "database.sqlite.path")
}

func TestGuardRefusesTheOldXDGLayout(t *testing.T) {
	home, d := guardFixture(t)
	if err := os.MkdirAll(filepath.Join(d.Data, "workspaces", "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	wantLegacy(t, RefuseLegacy(d, d.Database(), home), "workspaces/")
}

func TestGuardRefusesEntriesOfTheOldStateTree(t *testing.T) {
	for _, m := range oldStateMarkers {
		t.Run(m, func(t *testing.T) {
			home, d := guardFixture(t)
			if err := os.WriteFile(filepath.Join(d.Data, m), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			wantLegacy(t, RefuseLegacy(d, d.Database(), home), m)
		})
	}
}

func TestGuardRefusesTheOldSchemaAndLeavesItUnchanged(t *testing.T) {
	home, d := guardFixture(t)
	old := filepath.Join(t.TempDir(), "main.db")
	db, err := sql.Open("sqlite", old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
		INSERT INTO schema_migrations VALUES (8, '2026-10-30T18:46:47Z')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before, _ := os.ReadFile(old) //nolint:gosec // the test's own temp file

	wantLegacy(t, RefuseLegacy(d, old, home), "old build's schema")

	after, _ := os.ReadFile(old) //nolint:gosec // the test's own temp file
	if string(before) != string(after) {
		t.Fatal("the guard changed the database it judged")
	}
}

func TestGuardAcceptsANewDatabase(t *testing.T) {
	home, d := guardFixture(t)
	db, err := sql.Open("sqlite", d.Database())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE goose_db_version (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := RefuseLegacy(d, d.Database(), home); err != nil {
		t.Fatal(err)
	}
}
