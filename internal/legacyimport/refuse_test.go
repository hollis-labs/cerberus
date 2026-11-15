package legacyimport_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/legacyimport"
	"github.com/hollis-labs/cerberus/internal/legacyimport/legacyimporttest"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

// execOld runs statements on the backup copy (a test's own file, opened read-write).
func execOld(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// Each refusal happens in preflight: the run reports it, and nothing exists afterwards (no data
// dir, no staging, no config items, no new anchor), and the source is as it was.
func TestRefusalsWriteNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *legacyimporttest.Fixture, o *legacyimport.Options)
		want  string
		// keepsTarget: the case put something at the target itself; skip, in the config dir.
		keepsTarget bool
		skip        string
	}{
		{name: "non-empty target", keepsTarget: true, want: "is not empty", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			if err := os.MkdirAll(f.DataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.DataDir, "identity-signing.key"), []byte("x"), 0o600); err != nil { //nolint:gosec // a path in the test's own dir
				t.Fatal(err)
			}
		}},
		{name: "wrong schema set", want: "schema_migrations versions [1 2 3 4 5 6 7 8]", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			execOld(t, f.OldDB, `INSERT INTO schema_migrations (version, applied_at) VALUES (4, 'x')`)
		}},
		{name: "scheduler row", want: "gosched_schedules holds 1 row", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			execOld(t, f.OldDB, `CREATE TABLE gosched_schedules (id TEXT)`, `INSERT INTO gosched_schedules VALUES ('s1')`)
		}},
		{name: "project row", want: "projects holds 1 row", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			execOld(t, f.OldDB, `INSERT INTO projects (id, name, description, created_at, updated_at) VALUES ('p', 'P', '', 'x', 'x')`)
		}},
		{name: "unknown table", want: "does not know", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			execOld(t, f.OldDB, `CREATE TABLE notes (x TEXT)`)
		}},
		{name: "torn audit line", want: "ends in a torn line", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			appendFile(t, filepath.Join(f.OldHome, "audit", "2026-11.jsonl"), `{"seq":11,"kind":"inte`)
		}},
		{name: "missing digest key", want: "digest_key is missing", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			if err := os.Remove(filepath.Join(f.OldHome, "audit", ".digest_key")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed applied.json", want: "applied.json is not a JSON object", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			writeFile(t, filepath.Join(f.OldHome, "runtime", "applied.json"), `{"web": "hmac-sha256:aa",`)
		}},
		{name: "non-string digest", want: "is a float64, not a string", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			writeFile(t, filepath.Join(f.OldHome, "runtime", "applied.json"), `{"web": 7}`)
		}},
		{name: "missing old anchor", want: "there is no cerberus-audit-anchor keychain item", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			if err := secrets.KeyringDelete(auditchain.AnchorService, f.Account); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a different new anchor", want: "already holds a different", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			if err := secrets.KeyringSet(auditchain.AnchorService, filepath.Join(f.DataDir, "audit"), `{"seq":1,"hash":"sha256:00","file":"x.jsonl","offset":0}`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "standby.d in the snapshot", want: "standby.d", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			if err := os.Mkdir(filepath.Join(f.OldHome, "standby.d"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "old home at the live tree", want: "a live tree", setup: func(t *testing.T, f *legacyimporttest.Fixture, o *legacyimport.Options) {
			live := filepath.Join(f.Home, ".cerberus")
			legacyimporttest.CopyTree(t, live)
			o.OldHome = live
		}},
		{name: "old home is the home directory", want: "a live tree", setup: func(t *testing.T, f *legacyimporttest.Fixture, o *legacyimport.Options) {
			if err := os.MkdirAll(f.Home, 0o700); err != nil {
				t.Fatal(err)
			}
			o.OldHome = f.Home
		}},
		{name: "old db in the old XDG data dir", want: "a live tree", setup: func(t *testing.T, f *legacyimporttest.Fixture, o *legacyimport.Options) {
			live := filepath.Join(f.Home, ".local", "share", "cerberus", "workspaces", "default", "main.db")
			data, _ := os.ReadFile(f.OldDB) //nolint:gosec // a path in the test's own dir
			if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, live, string(data))
			o.OldDB = live
		}},
		{name: "existing policy in the config dir", want: "never replaces", skip: "policy", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			if err := os.MkdirAll(filepath.Join(f.ConfigDir, "policy"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "receipt state mismatch", want: "do not match its path", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			p := filepath.Join(f.OldHome, "credential-issuance", strings.Repeat("a", 32), "completed.json")
			data, _ := os.ReadFile(p) //nolint:gosec // a path in the test's own dir
			writeFile(t, p, strings.Replace(string(data), `"state":"completed"`, `"state":"uncertain"`, 1))
		}},
		{name: "a pause marker that is a directory", want: "a pause marker is a file", setup: func(t *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			if err := os.Mkdir(filepath.Join(f.OldHome, "paused.dir"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "an unknown config key", want: "unknown key", setup: func(_ *testing.T, f *legacyimporttest.Fixture, _ *legacyimport.Options) {
			appendFile(t, filepath.Join(f.OldHome, "config.yaml"), "surprise: true\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := legacyimporttest.Materialize(t, t.TempDir())
			o := options(f)
			tc.setup(t, &f, &o)
			before, err := legacyimport.TakeFingerprint(o.OldHome, o.OldDB)
			if err != nil {
				t.Fatal(err)
			}
			anchorBefore, _ := secrets.KeyringGet(auditchain.AnchorService, filepath.Join(o.DataDir, "audit"))
			rep, err := legacyimport.Run(context.Background(), o)
			if !errors.Is(err, legacyimport.ErrRefused) || rep.Result != legacyimport.ResultRefused {
				t.Fatalf("run: %v (%s), want a refusal; refusals %v", err, rep.Result, rep.Refusals)
			}
			if !anyContains(rep.Refusals, tc.want) {
				t.Fatalf("refusals %v, want one containing %q", rep.Refusals, tc.want)
			}
			if tc.keepsTarget {
				entries, _ := os.ReadDir(f.DataDir)
				if len(entries) != 1 {
					t.Fatalf("the target changed: %v", entries)
				}
				f.DataDir = filepath.Join(t.TempDir(), "unused")
			}
			if got, _ := secrets.KeyringGet(auditchain.AnchorService, filepath.Join(o.DataDir, "audit")); got != anchorBefore {
				t.Fatalf("the new anchor changed: %q, was %q", got, anchorBefore)
			}
			if tc.skip != "" {
				if fi, err := os.Stat(filepath.Join(f.ConfigDir, tc.skip)); err != nil || !fi.IsDir() {
					t.Fatalf("the config dir's %s changed", tc.skip)
				}
				f.ConfigDir = filepath.Join(t.TempDir(), "unused")
			}
			if anchorBefore == "" {
				assertNothingWritten(t, f)
			} else {
				f.DataDir = filepath.Join(t.TempDir(), "unused")
				assertNothingWritten(t, f)
			}
			if after, _ := legacyimport.TakeFingerprint(o.OldHome, o.OldDB); after != before {
				t.Fatal("a refused run changed the source")
			}
		})
	}
}

// A failure after the build started (here: the source changes under the run, which the final
// fingerprint catches) undoes the commit: no data dir, no config items, no new anchor.
func TestAFailedRunLeavesNothing(t *testing.T) {
	f := legacyimporttest.Materialize(t, t.TempDir())
	o := options(f)
	o.TestHookAfterBuild = func() {
		appendFile(t, filepath.Join(f.OldHome, "cerberus.log"), "written during the run\n")
	}
	rep, err := legacyimport.Run(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "the source changed") || rep.Result != legacyimport.ResultFailed || rep.SourceUnchanged {
		t.Fatalf("a source changed during the run: %v (%s)", err, rep.Result)
	}
	assertNothingWritten(t, f)
}

func writeFile(t *testing.T, p, data string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil { //nolint:gosec // a path in the test's own dir
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, p, data string) {
	t.Helper()
	fh, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // the test's own file
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
