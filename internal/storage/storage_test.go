package storage_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	"github.com/hollis-labs/libs/core/store/storetest"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

// open opens and migrates the SQLite file at path the way the database module does: libs
// core/store, then the embedded goose set. The read pool is one connection, so a read that
// waits for a second one hangs the test instead of passing.
func open(t *testing.T, path string) *store.Store {
	t.Helper()
	ctx := context.Background()
	cfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: path, CreateParentDir: true, ReadConns: 1}}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	fsys, root := storage.Migrations()
	if err := migrate.Ensure(ctx, cfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
		_ = st.Close()
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// envPostgres names the server the Postgres runs use (a private schema per test, dropped after).
const envPostgres = "SUBSTRATE_TEST_POSTGRES_DSN"

func TestMain(m *testing.M) {
	// storetest reads its own variable; CI and local runs set envPostgres.
	if dsn := os.Getenv(envPostgres); dsn != "" && os.Getenv(storetest.EnvDSN) == "" {
		_ = os.Setenv(storetest.EnvDSN, dsn)
	}
	testhome.Main(m)
}

// eachDialect runs fn on a migrated SQLite file and, when envPostgres is set, on a migrated
// private Postgres schema. Without it the Postgres run skips, saying so (CI always sets it).
func eachDialect(t *testing.T, fn func(t *testing.T, st *store.Store, reopen func() *store.Store)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "data", "cerberus.db")
		fn(t, open(t, path), func() *store.Store { return open(t, path) })
	})
	t.Run("postgres", func(t *testing.T) {
		if os.Getenv(storetest.EnvDSN) == "" && os.Getenv("CI") == "" {
			t.Skipf("%s not set; skipping the Postgres run of this test", envPostgres)
		}
		cfg := storetest.PGConfig(t)
		if cfg == nil {
			return
		}
		// Postgres defaults to verify-only; the tests migrate, as the app's migrate step will.
		cfg.SchemaMode = store.SchemaAuto
		openPG := func() *store.Store {
			ctx := context.Background()
			st, err := store.Open(ctx, *cfg)
			if err != nil {
				t.Fatalf("open postgres: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			fsys, root := storage.Migrations()
			if err := migrate.Ensure(ctx, *cfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
				t.Fatalf("migrate postgres: %v", err)
			}
			return st
		}
		fn(t, openPG(), openPG)
	})
}

func tables(t *testing.T, st *store.Store) []string {
	t.Helper()
	// Catalog queries are per dialect by nature; names are lowercase, so LIKE's case rule does not matter.
	// gostore:allow like-case
	q := `SELECT name FROM sqlite_master WHERE type IN ('table', 'index') AND name NOT LIKE 'sqlite_%' ORDER BY name`
	if st.Dialect().Name() == store.DriverPostgres {
		// gostore:allow like-case
		q = `SELECT relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'i') AND relname NOT LIKE '%_pkey' AND relname NOT LIKE '%_seq'
ORDER BY relname`
	}
	rows, err := st.Reader().QueryContext(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func gooseVersions(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.Reader().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM goose_db_version`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSchemaMigratesOnEmptyAndSecondStartIsNoOp(t *testing.T) {
	eachDialect(t, func(t *testing.T, st *store.Store, reopen func() *store.Store) {
		first, versions := tables(t, st), gooseVersions(t, st)
		want := []string{
			"applied_definitions", "applied_digest_key", "credential_issuance_receipts", "goose_db_version",
			"host_events", "host_events_host", "host_rollup_state", "host_sample_rollups", "host_samples",
			"host_status", "pause_state",
		}
		if !reflect.DeepEqual(first, want) {
			t.Fatalf("tables after first start = %v, want %v", first, want)
		}
		again := reopen()
		if got := tables(t, again); !reflect.DeepEqual(got, first) {
			t.Fatalf("second start changed the schema: %v, was %v", got, first)
		}
		if got := gooseVersions(t, again); got != versions {
			t.Fatalf("second start recorded more migrations: %d rows, was %d", got, versions)
		}
	})
}

// columns is PRAGMA table_info for table: name, type, notnull, default, pk.
func columns(t *testing.T, q store.Querier, table string) [][5]string {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), `SELECT name, type, "notnull", COALESCE(dflt_value, ''), pk FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out [][5]string
	for rows.Next() {
		var c [5]string
		if err := rows.Scan(&c[0], &c[1], &c[2], &c[3], &c[4]); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// SQLite only from here: the old build was SQLite, and the old host store is carried code.

// The host tables keep the old build's shape exactly, so the import copies rows unchanged. The
// old migrations are frozen in testdata/old (from internal/store/sqlite/migrations on main).
func TestHostTablesMatchTheOldSchema(t *testing.T) {
	ctx := context.Background()
	st := open(t, filepath.Join(t.TempDir(), "new.db"))
	defer func() { _ = st.Close() }()

	oldCfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: filepath.Join(t.TempDir(), "old.db")}}
	old, err := store.Open(ctx, oldCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	files, _ := filepath.Glob("testdata/old/*.sql")
	sort.Strings(files)
	if len(files) != 3 {
		t.Fatalf("want the 3 old host migrations in testdata/old, found %d", len(files))
	}
	for _, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // a test fixture in this package
		if err != nil {
			t.Fatal(err)
		}
		if _, err := old.ExecContext(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	for _, table := range []string{"host_samples", "host_status", "host_sample_rollups", "host_rollup_state", "host_events"} {
		if got, want := columns(t, st.Reader(), table), columns(t, old.Reader(), table); !reflect.DeepEqual(got, want) {
			t.Errorf("%s columns differ from the old build:\n new %v\n old %v", table, got, want)
		}
	}
}

// The old host store (internal/hosts) runs unchanged on the new schema.
func TestOldHostStoreRunsOnTheNewSchema(t *testing.T) {
	ctx := context.Background()
	st := open(t, filepath.Join(t.TempDir(), "cerberus.db"))
	defer func() { _ = st.Close() }()
	s := hosts.NewSampleStore(st)
	at := time.Unix(1_700_000_000, 0).UTC()
	load := 0.5
	if err := s.AppendSample(ctx, hosts.Sample{Host: "box-1", At: at, Load1: &load, CPU: 4, ProbeMS: 12}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Recent(ctx, "box-1", 5)
	if err != nil || len(got) != 1 || got[0].CPU != 4 || got[0].Load1 == nil || *got[0].Load1 != load {
		t.Fatalf("Recent = %+v, %v", got, err)
	}
}

func TestPauseState(t *testing.T) {
	eachDialect(t, func(t *testing.T, st *store.Store, _ func() *store.Store) {
		ctx := context.Background()
		now := time.Unix(1_700_000_000, 0)

		if p, err := storage.Paused(ctx, st, "web"); err != nil || p {
			t.Fatalf("fresh: paused=%v err=%v", p, err)
		}
		if err := storage.SetPaused(ctx, st, "web", true, now); err != nil {
			t.Fatal(err)
		}
		if err := storage.SetPaused(ctx, st, "web", true, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if p, _ := storage.Paused(ctx, st, "web"); !p {
			t.Fatal("web not paused")
		}
		if p, _ := storage.Paused(ctx, st, "api"); p {
			t.Fatal("api paused by web's marker")
		}
		if err := storage.SetPaused(ctx, st, storage.AllTargets, true, now); err != nil {
			t.Fatal(err)
		}
		if p, _ := storage.Paused(ctx, st, "api"); !p {
			t.Fatal("pause-all does not cover api")
		}
		if got, _ := storage.PausedTargets(ctx, st); !reflect.DeepEqual(got, []string{"*", "web"}) {
			t.Fatalf("targets = %v", got)
		}
		for _, target := range []string{"web", storage.AllTargets} {
			if err := storage.SetPaused(ctx, st, target, false, now); err != nil {
				t.Fatal(err)
			}
		}
		if p, _ := storage.Paused(ctx, st, "web"); p {
			t.Fatal("web still paused after resume")
		}
		if err := storage.SetPaused(ctx, st, "", true, now); err == nil {
			t.Fatal("empty target accepted")
		}
	})
}

func TestAppliedDefinitions(t *testing.T) {
	eachDialect(t, func(t *testing.T, st *store.Store, _ func() *store.Store) {
		ctx := context.Background()
		now := time.Unix(1_700_000_000, 0)

		if _, ok, err := storage.Applied(ctx, st, "web"); err != nil || ok {
			t.Fatalf("fresh: ok=%v err=%v", ok, err)
		}
		for _, d := range []string{"sha256:aaa", "sha256:aaa", "sha256:bbb"} {
			if err := storage.SetApplied(ctx, st, "web", d, now); err != nil {
				t.Fatal(err)
			}
		}
		if d, ok, err := storage.Applied(ctx, st, "web"); err != nil || !ok || d != "sha256:bbb" {
			t.Fatalf("Applied = %q %v %v", d, ok, err)
		}
		if err := storage.SetApplied(ctx, st, "web", "", now); err == nil {
			t.Fatal("empty digest accepted")
		}
	})
}

func TestReceiptsAreWrittenOnceAndReadInOrder(t *testing.T) {
	eachDialect(t, func(t *testing.T, st *store.Store, _ func() *store.Store) {
		ctx := context.Background()
		at := time.Date(2026, 11, 14, 12, 0, 0, 123456789, time.UTC)
		base := storage.Receipt{IssuanceID: "iss-1", Connector: "postgres", Operation: "provision", BindingDigest: "d1",
			References: map[string]string{"runtime": "file:///private/issued/iss-1/runtime"}}

		for i, state := range []string{"dispatching", "preparing", "prepared"} {
			r := base
			r.State, r.At = state, at.Add(time.Duration(i)*time.Second)
			if err := storage.RecordReceipt(ctx, st, r); err != nil {
				t.Fatalf("%s: %v", state, err)
			}
		}
		dup := base
		dup.State, dup.At = "prepared", at
		if err := storage.RecordReceipt(ctx, st, dup); !errors.Is(err, storage.ErrReceiptExists) {
			t.Fatalf("second prepared receipt: %v, want ErrReceiptExists", err)
		}
		bad := base
		bad.State, bad.At = "exploded", at
		if err := storage.RecordReceipt(ctx, st, bad); err == nil || (!strings.Contains(err.Error(), "CHECK") && !strings.Contains(err.Error(), "check constraint")) {
			t.Fatalf("unknown state: %v, want a CHECK failure", err)
		}

		got, err := storage.Receipts(ctx, st, "iss-1")
		if err != nil {
			t.Fatal(err)
		}
		var states []string
		for _, r := range got {
			states = append(states, r.State)
		}
		if !reflect.DeepEqual(states, []string{"preparing", "prepared", "dispatching"}) {
			t.Fatalf("states = %v", states)
		}
		if !got[0].At.Equal(at.Add(time.Second)) || got[0].References["runtime"] == "" {
			t.Fatalf("receipt did not round-trip: %+v", got[0])
		}
	})
}

// ReceiptsAll lists every issuance's receipts, by id and then in lifecycle order, on both
// dialects, with their references and times as recorded.
func TestReceiptsAllListsEveryIssuance(t *testing.T) {
	eachDialect(t, func(t *testing.T, st *store.Store, _ func() *store.Store) {
		ctx := context.Background()
		if got, err := storage.ReceiptsAll(ctx, st); err != nil || len(got) != 0 {
			t.Fatalf("empty store: %v %v", got, err)
		}
		at := time.Date(2026, 11, 14, 12, 0, 0, 123456789, time.UTC)
		for i, rc := range []struct{ id, state string }{{"iss-b", "prepared"}, {"iss-a", "completed"}, {"iss-b", "preparing"}, {"iss-a", "preparing"}, {"iss-a", "dispatching"}, {"iss-a", "prepared"}} {
			r := storage.Receipt{IssuanceID: rc.id, State: rc.state, Connector: "postgres", Operation: "provision", BindingDigest: "d-" + rc.id,
				At: at.Add(time.Duration(i) * time.Second)}
			if rc.state != "preparing" {
				r.References = map[string]string{"runtime": "file:///private/issued/" + rc.id + "/runtime"}
			}
			if err := storage.RecordReceipt(ctx, st, r); err != nil {
				t.Fatal(err)
			}
		}
		got, err := storage.ReceiptsAll(ctx, st)
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, r := range got {
			order = append(order, r.IssuanceID+"/"+r.State)
		}
		want := []string{"iss-a/preparing", "iss-a/prepared", "iss-a/dispatching", "iss-a/completed", "iss-b/preparing", "iss-b/prepared"}
		if !reflect.DeepEqual(order, want) {
			t.Fatalf("order %v, want %v", order, want)
		}
		if got[0].References == nil || len(got[0].References) != 0 || got[1].References["runtime"] == "" || !got[5].At.Equal(at) || got[0].BindingDigest != "d-iss-a" {
			t.Fatalf("receipts did not round-trip: %+v", got)
		}
	})
}

// Both dialects carry the same migration versions, so a class can move without a renumber.
func TestDialectsHaveTheSameMigrations(t *testing.T) {
	fsys, root := storage.Migrations()
	if err := migrate.CheckParity(migrate.Source{FS: fsys, Root: root}); err != nil {
		t.Fatal(err)
	}
}

// A data dir already at version 2 (00001 and 00002 applied, as substrate-port shipped them)
// migrates to 00003 with its rows kept: the digest-key table is a new file, never an edit of an
// applied one.
func TestADataDirAtVersionTwoMigratesForward(t *testing.T) {
	ctx := context.Background()
	fsys, root := storage.Migrations()
	v2 := fstest.MapFS{}
	for _, name := range []string{"00001_local.sql", "00002_telemetry.sql"} {
		raw, err := fs.ReadFile(fsys, root+"/sqlite/"+name)
		if err != nil {
			t.Fatal(err)
		}
		v2[root+"/sqlite/"+name] = &fstest.MapFile{Data: raw}
	}
	path := filepath.Join(t.TempDir(), "data", "cerberus.db")
	cfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: path, CreateParentDir: true, ReadConns: 1}}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate.Ensure(ctx, cfg, st, migrate.Source{FS: v2, Root: root}); err != nil {
		t.Fatalf("migrate to version 2: %v", err)
	}
	if err = storage.SetApplied(ctx, st, "web", "hmac-sha256:aa", time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	st = open(t, path)
	if n, err := storage.AppliedCount(ctx, st); err != nil || n != 1 {
		t.Fatalf("applied rows after the upgrade: %d %v", n, err)
	}
	if err := storage.SetAppliedKey(ctx, st, "hmac-sha256:bb", time.Now()); err != nil {
		t.Fatalf("the 00003 table is missing after the upgrade: %v", err)
	}
	if got := gooseVersions(t, st); got < 4 {
		t.Fatalf("goose rows %d after the upgrade", got)
	}
}

// The import writes the digests and the key's fingerprint together, verbatim, and refuses a store
// that already has either: a store never holds migrated digests without the fingerprint.
func TestImportAppliedWritesDigestsAndKeyTogether(t *testing.T) {
	eachDialect(t, func(t *testing.T, st *store.Store, _ func() *store.Store) {
		ctx := context.Background()
		at := time.Unix(1_700_000_000, 0)
		digests := map[string]string{"web": "hmac-sha256:aa", "api": "hmac-sha256:bb", "ghost.x": "hmac-sha256:cc"}

		for _, bad := range []struct {
			digests map[string]string
			fp      string
		}{{digests, ""}, {map[string]string{"": "hmac-sha256:aa"}, "hmac-sha256:ff"}, {map[string]string{"web": ""}, "hmac-sha256:ff"}} {
			if err := storage.ImportApplied(ctx, st, bad.digests, bad.fp, at); err == nil {
				t.Fatalf("ImportApplied(%v, %q) accepted", bad.digests, bad.fp)
			}
		}
		if n, _ := storage.AppliedCount(ctx, st); n != 0 {
			t.Fatalf("a refused import wrote %d digests", n)
		}
		if err := storage.ImportApplied(ctx, st, digests, "hmac-sha256:ff", at); err != nil {
			t.Fatal(err)
		}
		got, err := storage.AppliedAll(ctx, st)
		if err != nil || !reflect.DeepEqual(got, digests) {
			t.Fatalf("AppliedAll = %v %v, want %v", got, err, digests)
		}
		if fp, ok, err := storage.AppliedKey(ctx, st); err != nil || !ok || fp != "hmac-sha256:ff" {
			t.Fatalf("AppliedKey = %q %v %v", fp, ok, err)
		}
		if err := storage.ImportApplied(ctx, st, map[string]string{"other": "hmac-sha256:dd"}, "hmac-sha256:ff", at); !errors.Is(err, storage.ErrAppliedNotEmpty) {
			t.Fatalf("second import: %v, want ErrAppliedNotEmpty", err)
		}
		if got, _ := storage.AppliedAll(ctx, st); !reflect.DeepEqual(got, digests) {
			t.Fatalf("a refused second import changed the digests: %v", got)
		}
	})
	// A store holding only a fingerprint (a supervise start before the import) is refused too.
	eachDialect(t, func(t *testing.T, st *store.Store, _ func() *store.Store) {
		ctx := context.Background()
		if err := storage.SetAppliedKey(ctx, st, "hmac-sha256:ee", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := storage.ImportApplied(ctx, st, map[string]string{"web": "hmac-sha256:aa"}, "hmac-sha256:ff", time.Now()); !errors.Is(err, storage.ErrAppliedNotEmpty) {
			t.Fatalf("import over a recorded key: %v", err)
		}
		if n, _ := storage.AppliedCount(ctx, st); n != 0 {
			t.Fatalf("refused import wrote %d digests", n)
		}
	})
}
