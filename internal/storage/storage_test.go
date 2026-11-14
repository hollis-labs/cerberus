package storage_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/storage"
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

func tables(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.Reader().QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type IN ('table', 'index') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
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
	path := filepath.Join(t.TempDir(), "data", "cerberus.db")
	st := open(t, path)
	first, versions := tables(t, st), gooseVersions(t, st)
	_ = st.Close()

	want := []string{
		"applied_definitions", "credential_issuance_receipts", "goose_db_version",
		"host_events", "host_events_host", "host_rollup_state", "host_sample_rollups", "host_samples",
		"host_status", "pause_state",
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("tables after first start = %v, want %v", first, want)
	}

	st = open(t, path)
	defer func() { _ = st.Close() }()
	if got := tables(t, st); !reflect.DeepEqual(got, first) {
		t.Fatalf("second start changed the schema: %v, was %v", got, first)
	}
	if got := gooseVersions(t, st); got != versions {
		t.Fatalf("second start recorded more migrations: %d rows, was %d", got, versions)
	}
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
	ctx := context.Background()
	st := open(t, filepath.Join(t.TempDir(), "cerberus.db"))
	defer func() { _ = st.Close() }()
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
}

func TestAppliedDefinitions(t *testing.T) {
	ctx := context.Background()
	st := open(t, filepath.Join(t.TempDir(), "cerberus.db"))
	defer func() { _ = st.Close() }()
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
}

func TestReceiptsAreWrittenOnceAndReadInOrder(t *testing.T) {
	ctx := context.Background()
	st := open(t, filepath.Join(t.TempDir(), "cerberus.db"))
	defer func() { _ = st.Close() }()
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
	if err := storage.RecordReceipt(ctx, st, bad); err == nil || !strings.Contains(err.Error(), "CHECK") {
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
}
