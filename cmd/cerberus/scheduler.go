package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/scheduling"
	"github.com/hollis-labs/cerberus/internal/settings"
)

// schedulerLoop is the scheduling core, built as main built it (no executor or authorizer, so a
// fire is refused rather than run) and started only when cerberus.scheduler.enabled. Off, it is
// not constructed at all, so it creates no table. Substrate's scheduler module is not used: it
// builds its own engine over the same tables (docs/conversion-notes.md, post-adoption).
//
// An enabled scheduler refuses to start while its tables already hold rows, unless
// cerberus.scheduler.allow_existing is set: those rows are schedules nobody has reviewed for
// this build (plan section 5, the C tables are asserted empty at migration).
func schedulerLoop(ctx context.Context, local *store.Store, st settings.Settings) (*loop, error) {
	if !st.Scheduler.Enabled {
		return nil, nil
	}
	if local.Dialect().Name() != store.DriverSQLite {
		return nil, fmt.Errorf("%s.scheduler: the scheduling core runs on the local SQLite store only; its tables (class C) have not moved", settings.Key)
	}
	db := local.DB()
	rows, err := scheduleRows(ctx, db)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 && !st.Scheduler.AllowExisting {
		return nil, fmt.Errorf("%s.scheduler: refusing to start: its tables already hold rows (%s); review them, then set %s.scheduler.allow_existing: true",
			settings.Key, strings.Join(rows, ", "), settings.Key)
	}
	core, err := scheduling.New(ctx, db, nil, nil, scheduling.Options{})
	if err != nil {
		return nil, fmt.Errorf("%s.scheduler: %w", settings.Key, err)
	}
	return &loop{name: "scheduler", run: func(ctx context.Context) {
		core.Start()
		<-ctx.Done()
		core.Stop()
	}}, nil
}

// scheduleRows lists the scheduler's tables that hold rows, as "table=count", sorted.
func scheduleRows(ctx context.Context, db *sql.DB) ([]string, error) {
	q, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for q.Next() {
		var name string
		if err := q.Scan(&name); err != nil {
			_ = q.Close()
			return nil, err
		}
		if strings.HasPrefix(name, "gosched_") || strings.HasPrefix(name, "cerberus_schedule_") {
			tables = append(tables, name)
		}
	}
	_ = q.Close()
	if err := q.Err(); err != nil {
		return nil, err
	}
	sort.Strings(tables)
	var out []string
	for _, t := range tables {
		var n int64
		// The names come from sqlite_master and carry the scheduler's prefixes.
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+t+`"`).Scan(&n); err != nil { //nolint:gosec // table names from sqlite_master
			return nil, err
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("%s=%d", t, n))
		}
	}
	return out, nil
}
