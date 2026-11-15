package legacyimport

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/legacyimport/hostdump"
)

// wantVersions is the old schema_migrations set the import accepts (R3): 4 is the bootstrap the
// old store never records, and 003's table was dropped by 005.
var wantVersions = []int{1, 2, 3, 5, 6, 7, 8}

// emptyTables are the old tables that must hold no rows (R2): an absent table holds none. Any
// other table whose name starts with one of emptyPrefixes is held to the same rule.
var (
	emptyTables = []string{
		"projects", "resources",
		"cerberus_schedule_creates", "cerberus_schedule_desired", "cerberus_schedule_episodes", "cerberus_schedule_generations",
		"cerberus_schedule_logs", "cerberus_schedule_manual", "cerberus_schedule_observations", "cerberus_schedule_outbox",
		"cerberus_schedule_outcomes", "cerberus_schedule_receipts", "cerberus_schedule_registrations",
		"gosched_fire_options", "gosched_fires", "gosched_pruned", "gosched_schedule_options", "gosched_schedules",
	}
	emptyPrefixes = []string{"cerberus_schedule_", "gosched_"}
)

// knownOldTables are the tables an old main.db may hold: the host tables, the asserted-empty
// ones, and the bookkeeping. Any other table is refused, so no row is left behind unseen.
func knownOldTable(name string) bool {
	switch name {
	case "schema_migrations", "sqlite_sequence", "sqlite_stat1", "sqlite_stat4":
		return true
	}
	for _, t := range hostdump.Tables {
		if t.Name == name {
			return true
		}
	}
	for _, t := range emptyTables {
		if t == name {
			return true
		}
	}
	for _, p := range emptyPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// inspectOldDB is the preflight of R1 to R3: integrity, the schema set, the asserted-empty
// tables, the host tables' columns and the sequence. It opens the backup immutable and changes
// nothing. Refusals go to r; an error is a database that cannot be read at all.
func inspectOldDB(ctx context.Context, path string, r *Report) (*MainDBReport, error) {
	db, err := hostdump.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open --old-db %s read-only: %w", path, err)
	}
	defer func() { _ = db.Close() }()
	rep := &MainDBReport{Tables: map[string]TableReport{}, AssertedEmpty: map[string]int64{}}

	var checks []string
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return nil, fmt.Errorf("--old-db %s: integrity_check: %w", path, err)
	}
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			_ = rows.Close()
			return nil, err
		}
		checks = append(checks, s)
	}
	_ = rows.Close()
	rep.IntegrityCheck = strings.Join(checks, "; ")
	if rep.IntegrityCheck != "ok" {
		r.refuse("--old-db %s fails PRAGMA integrity_check: %s", path, rep.IntegrityCheck)
		return rep, nil
	}

	tables := map[string]bool{}
	rows, err = db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tables[n] = true
	}
	_ = rows.Close()

	if !tables["schema_migrations"] {
		r.refuse("--old-db %s has no schema_migrations table: it is not the old build's main.db", path)
		return rep, nil
	}
	rows, err = db.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY 1")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v int
		if err = rows.Scan(&v); err != nil {
			_ = rows.Close()
			return nil, err
		}
		rep.SchemaVersions = append(rep.SchemaVersions, v)
	}
	_ = rows.Close()
	if !reflect.DeepEqual(rep.SchemaVersions, wantVersions) {
		r.refuse("--old-db %s has schema_migrations versions %v; the import accepts exactly %v (main's migrations 001 to 008, 004 being the bootstrap)", path, rep.SchemaVersions, wantVersions)
	}

	var unknown []string
	for name := range tables {
		if !knownOldTable(name) {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		r.refuse("--old-db %s holds table(s) the import does not know, which would be left behind: %s", path, strings.Join(unknown, ", "))
	}

	names := append([]string(nil), emptyTables...)
	for name := range tables {
		for _, p := range emptyPrefixes {
			if strings.HasPrefix(name, p) && !containsString(names, name) {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var n int64
		if tables[name] {
			if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+hostdump.Quote(name)).Scan(&n); err != nil {
				return nil, err
			}
		}
		rep.AssertedEmpty[name] = n
		if n > 0 {
			r.refuse("--old-db %s: %s holds %d row(s); the import requires it empty (plan section 5: it becomes a transform first)", path, name, n)
		}
	}

	for _, t := range hostdump.Tables {
		if !tables[t.Name] {
			r.refuse("--old-db %s has no %s table", path, t.Name)
			continue
		}
		cols, cerr := tableColumns(ctx, db, t.Name)
		if cerr != nil {
			return nil, cerr
		}
		want := append([]string(nil), t.Columns...)
		sort.Strings(want)
		sort.Strings(cols)
		if !reflect.DeepEqual(cols, want) {
			r.refuse("--old-db %s: %s has columns %v, the import expects %v", path, t.Name, cols, want)
		}
	}
	if len(r.Refusals) > 0 {
		return rep, nil
	}

	st, err := hostdump.ReadState(ctx, db, "")
	if err != nil {
		return nil, fmt.Errorf("--old-db %s: %w", path, err)
	}
	if seq, ok := st.SQLiteSequence[hostdump.SequenceTable]; ok && st.HostEventsMaxID != nil && seq < *st.HostEventsMaxID {
		r.refuse("--old-db %s: sqlite_sequence for host_events is %d, below max(id) %d", path, seq, *st.HostEventsMaxID)
	}
	rep.SQLiteSequence.Old = st.SQLiteSequence
	rep.HostDumpSHA256.Old = st.HostDumpSHA256
	rep.MaxID = st.HostEventsMaxID
	for _, t := range hostdump.Tables {
		rep.Tables[t.Name] = TableReport{Before: st.Tables[t.Name].Count, MaxTime: st.Tables[t.Name].MaxTime}
	}
	return rep, nil
}

func tableColumns(ctx context.Context, q hostdump.Querier, table string) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// importHostTables copies every row of the five host tables, ids included, into the Telemetry
// store in ONE transaction, and sets host_events' sqlite_sequence to the old value (which may be
// past max(id) after the janitor deleted the newest rows). host_status's rowids are not carried:
// nothing reads them.
func importHostTables(ctx context.Context, st *store.Store, oldPath string, oldSeq map[string]int64) error {
	if st.Dialect().Name() != store.DriverSQLite {
		return fmt.Errorf("the host tables import into a SQLite store; the Telemetry store is %s", st.Dialect().Name())
	}
	old, err := hostdump.Open(oldPath)
	if err != nil {
		return err
	}
	defer func() { _ = old.Close() }()
	return st.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		for _, t := range hostdump.Tables {
			marks := strings.TrimSuffix(strings.Repeat("?, ", len(t.Columns)), ", ")
			insert := "INSERT INTO " + hostdump.Quote(t.Name) + " (" + t.ColumnList() + ") VALUES (" + marks + ")"
			if err := hostdump.Rows(ctx, old, "", t, func(vals []any) error {
				_, err := tx.ExecContext(ctx, insert, vals...)
				return err
			}); err != nil {
				return fmt.Errorf("import %s: %w", t.Name, err)
			}
		}
		seq, ok := oldSeq[hostdump.SequenceTable]
		if !ok {
			return nil
		}
		res, err := tx.ExecContext(ctx, "UPDATE sqlite_sequence SET seq = ? WHERE name = ?", seq, hostdump.SequenceTable)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			_, err = tx.ExecContext(ctx, "INSERT INTO sqlite_sequence (name, seq) VALUES (?, ?)", hostdump.SequenceTable, seq)
		}
		return err
	})
}

// verifyHostTables opens the new database read-only, attaches the old backup (immutable), and
// compares: counts, EXCEPT both ways, the logical state with its ordered digest, the sequence.
func verifyHostTables(ctx context.Context, newPath, oldPath string, rep *MainDBReport) error {
	// Read-only through the new database's WAL (immutable would miss what is not checkpointed).
	db, err := hostdump.OpenLive(newPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "ATTACH DATABASE ? AS old", hostdump.URI(oldPath, "mode=ro&immutable=1")); err != nil {
		return fmt.Errorf("attach the old backup read-only: %w", err)
	}
	var problems []string
	for _, t := range hostdump.Tables {
		tr := rep.Tables[t.Name]
		if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM main."+hostdump.Quote(t.Name)).Scan(&tr.After); err != nil {
			return err
		}
		var oldCount int64
		if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM old."+hostdump.Quote(t.Name)).Scan(&oldCount); err != nil {
			return err
		}
		cols := t.ColumnList()
		for _, dir := range []struct {
			a, b string
			dst  *int64
		}{{"old", "main", &tr.ExceptOldNew}, {"main", "old", &tr.ExceptNewOld}} {
			q := "SELECT COUNT(*) FROM (SELECT " + cols + " FROM " + dir.a + "." + hostdump.Quote(t.Name) + " EXCEPT SELECT " + cols + " FROM " + dir.b + "." + hostdump.Quote(t.Name) + ")"
			if err = conn.QueryRowContext(ctx, q).Scan(dir.dst); err != nil {
				return err
			}
		}
		if tr.After != oldCount || tr.Before != oldCount || tr.ExceptOldNew != 0 || tr.ExceptNewOld != 0 {
			problems = append(problems, fmt.Sprintf("%s: %d rows -> %d, except old-new %d, new-old %d", t.Name, oldCount, tr.After, tr.ExceptOldNew, tr.ExceptNewOld))
		}
		rep.Tables[t.Name] = tr
	}
	oldState, err := hostdump.ReadState(ctx, conn, "old")
	if err != nil {
		return err
	}
	newState, err := hostdump.ReadState(ctx, conn, "main")
	if err != nil {
		return err
	}
	rep.SQLiteSequence.New = onlySequence(newState.SQLiteSequence)
	rep.HostDumpSHA256.New = newState.HostDumpSHA256
	if oldState.HostDumpSHA256 != newState.HostDumpSHA256 || oldState.HostDumpSHA256 != rep.HostDumpSHA256.Old {
		problems = append(problems, "the ordered host rows digest differs: "+oldState.HostDumpSHA256+" -> "+newState.HostDumpSHA256)
	}
	if o, ok := oldState.SQLiteSequence[hostdump.SequenceTable]; ok && newState.SQLiteSequence[hostdump.SequenceTable] != o {
		problems = append(problems, fmt.Sprintf("host_events sqlite_sequence %d -> %d", o, newState.SQLiteSequence[hostdump.SequenceTable]))
	}
	// Only host_events' sequence is the old build's; the new database has goose's own.
	oldState.SQLiteSequence, newState.SQLiteSequence = onlySequence(oldState.SQLiteSequence), onlySequence(newState.SQLiteSequence)
	if d := oldState.Diff(newState); d != "" {
		problems = append(problems, d)
	}
	if len(problems) > 0 {
		return fmt.Errorf("the host tables do not match the old backup: %s", strings.Join(problems, "; "))
	}
	return nil
}

func onlySequence(m map[string]int64) map[string]int64 {
	out := map[string]int64{}
	if v, ok := m[hostdump.SequenceTable]; ok {
		out[hostdump.SequenceTable] = v
	}
	return out
}
