package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/settings"
)

func localStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Config{Driver: store.DriverSQLite,
		SQLite: store.SQLiteConfig{Path: filepath.Join(t.TempDir(), "cerberus.db"), CreateParentDir: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func tableCount(t *testing.T, st *store.Store, prefix string) int {
	t.Helper()
	rows, err := st.DB().Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(name, prefix) {
			n++
		}
	}
	return n
}

// Off by default: the scheduler is not even constructed, so it creates no table. Enabling it is
// config only.
func TestSchedulerOffByDefaultAndConfigOnly(t *testing.T) {
	ctx := context.Background()
	st := localStore(t)
	l, err := schedulerLoop(ctx, st, settings.Settings{})
	if err != nil || l != nil {
		t.Fatalf("off: %v %v", l, err)
	}
	if n := tableCount(t, st, "gosched_"); n != 0 {
		t.Fatalf("an off scheduler created %d tables", n)
	}
	l, err = schedulerLoop(ctx, st, settings.Settings{Scheduler: settings.Scheduler{Enabled: true}})
	if err != nil || l == nil || l.name != "scheduler" {
		t.Fatalf("on: %v %v", l, err)
	}
	if tableCount(t, st, "gosched_") == 0 || tableCount(t, st, "cerberus_schedule_") == 0 {
		t.Fatal("an enabled scheduler did not create its tables")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { l.run(runCtx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduler did not stop")
	}
}

// An enabled scheduler refuses to start over tables that hold rows, naming them, unless
// allow_existing is set.
func TestSchedulerRefusesExistingRows(t *testing.T) {
	ctx := context.Background()
	st := localStore(t)
	if _, err := schedulerLoop(ctx, st, settings.Settings{Scheduler: settings.Scheduler{Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO cerberus_schedule_generations(job_key, generation) VALUES ('cerberus/x', 1)`); err != nil {
		t.Fatal(err)
	}
	_, err := schedulerLoop(ctx, st, settings.Settings{Scheduler: settings.Scheduler{Enabled: true}})
	if err == nil || !strings.Contains(err.Error(), "cerberus_schedule_generations=1") || !strings.Contains(err.Error(), "allow_existing") {
		t.Fatalf("want a refusal naming the table and the flag, got %v", err)
	}
	if l, err := schedulerLoop(ctx, st, settings.Settings{Scheduler: settings.Scheduler{Enabled: true, AllowExisting: true}}); err != nil || l == nil {
		t.Fatalf("allow_existing: %v %v", l, err)
	}
}

// Event export is off by default and creates nothing; enabled, it writes its outbox under the
// data directory, never under the old tree.
func TestEventExportOffByDefaultAndConfigOnly(t *testing.T) {
	home := t.TempDir()
	dirs := datadir.Dirs{Data: filepath.Join(home, ".local", "share", "cerberus-substrate")}
	if err := os.MkdirAll(dirs.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	if loops := exportLoops(settings.Settings{}, dirs, nil); len(loops) != 0 {
		t.Fatalf("off: %d loops", len(loops))
	}
	if _, err := os.Stat(filepath.Join(dirs.Data, "events")); !os.IsNotExist(err) {
		t.Fatalf("an off export touched the outbox: %v", err)
	}
	loops := exportLoops(settings.Settings{EventExport: settings.EventExport{Enabled: true}}, dirs, nil)
	if len(loops) != 1 || loops[0].name != "event_export" {
		t.Fatalf("on: %+v", loops)
	}
	if _, err := os.Stat(filepath.Join(dirs.Data, "events")); err != nil {
		t.Fatalf("the outbox is not in the data directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".cerberus")); !os.IsNotExist(err) {
		t.Fatal("export touched the old tree")
	}
}
