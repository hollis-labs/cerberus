package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/hosts"
	_ "modernc.org/sqlite"
)

func versions(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// The database opens in WAL with foreign keys on, through core/store.
func TestOpenUsesWALAndForeignKeys(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sub", "cerberus.db")) // the directory is created
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck
	var mode string
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q %v", mode, err)
	}
	var fk int
	if err := s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d %v", fk, err)
	}
	var timeout int
	if err := s.DB().QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil || timeout < 5000 {
		t.Fatalf("busy_timeout = %d %v", timeout, err)
	}
	// A resource must belong to a project.
	err = s.SaveResource(context.Background(), &domain.Resource{ID: "r", Name: "r", Type: "process", ProjectID: "nope", Connector: "local"})
	if err == nil {
		t.Fatal("foreign keys are not enforced")
	}
}

// A database written before this change (opened with a bare sql.Open, no
// core/store) opens, keeps its data, and gets only the migrations it lacks.
func TestAnExistingDatabaseUpgradesInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cerberus.db")
	old, err := sql.Open("sqlite", path) // the previous open path
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(old); err != nil {
		t.Fatal(err)
	}
	// Pretend it predates the host monitoring migrations (6 is the last E1 one).
	for _, q := range []string{`DROP TABLE host_sample_rollups`, `DROP TABLE host_rollup_state`, `DROP TABLE host_events`, `DELETE FROM schema_migrations WHERE version > 6`} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := old.Exec(`INSERT INTO projects (id, name, description, created_at, updated_at) VALUES ('p','P','d','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO host_samples (host, ts, cpu) VALUES ('h', 100, 4)`); err != nil {
		t.Fatal(err)
	}
	before := versions(t, old)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("an existing database must open: %v", err)
	}
	defer s.Close() //nolint:errcheck
	after := versions(t, s.DB())
	if len(after) <= len(before) || after[len(after)-1] < 8 {
		t.Fatalf("migrations before %v, after %v", before, after)
	}
	p, err := s.GetProject(context.Background(), "p")
	if err != nil || p.Name != "P" {
		t.Fatalf("project lost: %v %v", p, err)
	}
	var cpu int
	if err := s.DB().QueryRow(`SELECT cpu FROM host_samples WHERE host='h'`).Scan(&cpu); err != nil || cpu != 4 {
		t.Fatalf("sample lost: %d %v", cpu, err)
	}
	// Re-opening is a no-op.
	s.Close() //nolint:errcheck
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close() //nolint:errcheck
	if again := versions(t, s2.DB()); fmt.Sprint(again) != fmt.Sprint(after) {
		t.Fatalf("a second open changed the versions: %v -> %v", after, again)
	}
}

// Several writers at once (the daemon's scheduler, monitor goroutines, domain
// saves) lose nothing and never see SQLITE_BUSY.
func TestConcurrentWritersLoseNothing(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cerberus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck
	ctx := context.Background()
	const writers, each = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				id := fmt.Sprintf("p-%d-%d", w, i)
				if err := s.SaveProject(ctx, &domain.Project{ID: id, Name: id}); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("write failed: %v", err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&n); err != nil || n != writers*each {
		t.Fatalf("%d projects, want %d (%v)", n, writers*each, err)
	}
}

// An optional check against a copy of a real database: set CERB_LIVE_COPY to a
// file (a copy, never the live one).
func TestLiveDatabaseCopy(t *testing.T) {
	src := os.Getenv("CERB_LIVE_COPY")
	if src == "" {
		t.Skip("set CERB_LIVE_COPY to a copy of a real cerberus database")
	}
	data, err := os.ReadFile(src) //nolint:gosec // an explicitly named copy
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("the copy did not open: %v", err)
	}
	defer s.Close() //nolint:errcheck
	t.Logf("migrations after upgrade: %v", versions(t, s.DB()))
	var check string
	if err := s.DB().QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q %v", check, err)
	}
	// The host stores work on it: compact, then read the rollups back.
	hs := hosts.NewSampleStore(s.Core())
	rep, err := hs.Compact(context.Background(), time.Now(), hosts.Retention{Raw: 7 * 24 * time.Hour, Rollup5m: 30 * 24 * time.Hour, Rollup1h: 400 * 24 * time.Hour, Events: 90 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("compact on the copy: %v", err)
	}
	t.Logf("compact: %s", rep)
	if err := hs.AppendSample(context.Background(), hosts.Sample{Host: "live-copy-probe", At: time.Now(), CPU: 1}); err != nil {
		t.Fatalf("append on the copy: %v", err)
	}
	for _, table := range []string{"projects", "resources", "host_samples", "host_status", "host_events", "host_sample_rollups", "gosched_schedules"} {
		var n int
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Logf("%s: %v", table, err)
			continue
		}
		t.Logf("%s: %d rows", table, n)
	}
}
