package legacyexport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hollis-labs/cerberus/internal/legacyimport/hostdump"
)

// busyTimeoutMS bounds waiting for old's database lock (a live old build may hold it briefly).
const busyTimeoutMS = 10000

// openTelemetry is the default TelemetryOpener: new's database, read-only through its WAL.
func (x *export) openTelemetry(context.Context) (hostdump.Querier, func() error, error) {
	db, err := hostdump.OpenLive(x.o.DBPath)
	if err != nil {
		return nil, nil, err
	}
	return db, db.Close, nil
}

// telemetry is the class B step, last: new's host rows merged into old's main.db in one
// transaction. When the Telemetry store cannot be read, or a row conflicts, the step is
// recorded pending and the run is not a success (exit 3); `--only telemetry` finishes it.
func (x *export) telemetry(ctx context.Context) error {
	s := x.state.Steps[StepTelemetry]
	if s.done() {
		x.r.step(StepTelemetry, StatusDone, false, "done by an earlier run; main.db is in its post state")
		return nil
	}
	rep := &TelemetryReport{Inserted: map[string]int64{}, Updated: map[string]int64{}, KeptOld: map[string]int64{}}
	x.r.Telemetry = rep
	x.state.Steps[StepTelemetry] = &Step{Status: StatusStarted, At: x.now().UTC()}
	if err := x.save(); err != nil {
		return err
	}
	open := x.o.Telemetry
	if open == nil {
		open = x.openTelemetry
	}
	q, closeFn, err := open(ctx)
	if err != nil {
		return x.pending(rep, fmt.Sprintf("the Telemetry store is unreachable: %v", err))
	}
	defer func() { _ = closeFn() }()
	post, err := mergeHostTables(ctx, q, x.o.OldDB, manifestDB{x.m.DB}, rep)
	if err != nil {
		return x.pending(rep, err.Error())
	}
	rep.HostDumpSHA256 = post.HostDumpSHA256
	x.state.Steps[StepTelemetry] = &Step{Status: StatusDone, At: x.now().UTC(), Post: &Post{DBState: &post}}
	if err := x.save(); err != nil {
		return err
	}
	x.r.step(StepTelemetry, StatusDone, true, "inserted %v, updated %v, kept old's newer %v; integrity_check %s", rep.Inserted, rep.Updated, rep.KeptOld, rep.IntegrityCheck)
	return nil
}

func (x *export) pending(rep *TelemetryReport, cause string) error {
	// Nothing was merged: the transaction rolled back (or never began).
	rep.Inserted, rep.Updated, rep.KeptOld = map[string]int64{}, map[string]int64{}, map[string]int64{}
	rep.Pending, rep.Error = true, cause
	x.state.Steps[StepTelemetry] = &Step{Status: StatusPending, At: x.now().UTC(), Error: cause}
	x.state.Error = cause
	if err := x.save(); err != nil {
		return err
	}
	x.r.step(StepTelemetry, StatusPending, true, "%s", cause)
	return &PendingError{Handoff: x.m.BodySHA256, Cause: cause}
}

// hostRow is one row: its values, its primary key and its tagged form (the comparison
// hostdump's digest uses: an integer and a real differ, so do NULL and "").
type hostRow struct {
	vals []any
	key  string
	tag  string
}

func readRows(ctx context.Context, q hostdump.Querier, t hostdump.Table) ([]hostRow, map[string]hostRow, error) {
	var list []hostRow
	byKey := map[string]hostRow{}
	keyIdx := keyIndexes(t)
	err := hostdump.Rows(ctx, q, "", t, func(vals []any) error {
		tags := make([]string, len(vals))
		for i, v := range vals {
			s, err := hostdump.Tag(v)
			if err != nil {
				return fmt.Errorf("%s: %w", t.Name, err)
			}
			tags[i] = s
		}
		keys := make([]string, len(keyIdx))
		for i, k := range keyIdx {
			keys[i] = tags[k]
		}
		kj, _ := json.Marshal(keys)
		tj, _ := json.Marshal(tags)
		row := hostRow{vals: vals, key: string(kj), tag: string(tj)}
		list = append(list, row)
		byKey[row.key] = row
		return nil
	})
	return list, byKey, err
}

func keyIndexes(t hostdump.Table) []int {
	var out []int
	for _, k := range t.Key {
		for i, c := range t.Columns {
			if c == k {
				out = append(out, i)
			}
		}
	}
	return out
}

func colIndex(t hostdump.Table, name string) int {
	for i, c := range t.Columns {
		if c == name {
			return i
		}
	}
	return -1
}

func tableNamed(name string) hostdump.Table {
	for _, t := range hostdump.Tables {
		if t.Name == name {
			return t
		}
	}
	panic("legacyexport: no host table " + name)
}

// intValue is an integer column's value (ok false for NULL or another type).
func intValue(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		return int64(x), true
	}
	return 0, false
}

// conflict is a row the merge refuses: the same key on both sides with different values where
// the rule says they cannot differ.
func conflict(t hostdump.Table, key, why string) error {
	return fmt.Errorf("class B conflict: %s %v = %s: %s; nothing was merged", t.Name, t.Key, key, why)
}

// mergeHostTables is R1 in reverse: an anti-join on each host table's primary key
// (hostdump.Tables), in ONE transaction on old's main.db (BEGIN IMMEDIATE, a busy timeout),
// verified before it commits. Rows new's janitor deleted stay in old: the result is a union.
func mergeHostTables(ctx context.Context, q hostdump.Querier, oldPath string, m dbFacts, rep *TelemetryReport) (hostdump.State, error) {
	db, err := sql.Open("sqlite", hostdump.URI(oldPath, ""))
	if err != nil {
		return hostdump.State{}, err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return hostdump.State{}, fmt.Errorf("open old's main.db %s: %w", oldPath, err)
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMS)); err != nil {
		return hostdump.State{}, err
	}

	// new's rows, read before old is locked.
	newRows := map[string][]hostRow{}
	for _, t := range hostdump.Tables {
		list, _, rerr := readRows(ctx, q, t)
		if rerr != nil {
			return hostdump.State{}, fmt.Errorf("read new's %s: %w", t.Name, rerr)
		}
		newRows[t.Name] = list
	}
	newSeqs, err := hostdump.Sequences(ctx, q, "")
	if err != nil {
		return hostdump.State{}, fmt.Errorf("read new's sqlite_sequence: %w", err)
	}

	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return hostdump.State{}, fmt.Errorf("lock old's main.db %s: %w", oldPath, err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	merge := &merger{ctx: ctx, conn: conn, rep: rep}
	if err = merge.run(newRows, newSeqs, m); err != nil {
		return hostdump.State{}, err
	}
	if err = merge.verify(newRows, newSeqs); err != nil {
		return hostdump.State{}, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return hostdump.State{}, fmt.Errorf("commit old's main.db: %w", err)
	}
	committed = true
	return hostdump.ReadState(ctx, conn, "")
}

type merger struct {
	ctx  context.Context
	conn *sql.Conn
	rep  *TelemetryReport
	// expected is, per table, what old must hold for each key new has after the merge (status
	// and rollup state); the append tables need only new's rows present.
	expected map[string]map[string]string
}

func (g *merger) insert(t hostdump.Table, vals []any) error {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(t.Columns)), ", ")
	// Identifiers from hostdump.Tables, quoted; every value is a bound parameter.
	_, err := g.conn.ExecContext(g.ctx, "INSERT INTO "+hostdump.Quote(t.Name)+" ("+t.ColumnList()+") VALUES ("+marks+")", vals...) //nolint:gosec // as above
	if err == nil {
		g.rep.Inserted[t.Name]++
	}
	return err
}

func (g *merger) update(t hostdump.Table, vals []any) error {
	keyIdx := keyIndexes(t)
	var sets, where []string
	var args, keyArgs []any
	for i, c := range t.Columns {
		isKey := false
		for _, k := range keyIdx {
			isKey = isKey || k == i
		}
		if isKey {
			where = append(where, hostdump.Quote(c)+" = ?")
			keyArgs = append(keyArgs, vals[i])
			continue
		}
		sets = append(sets, hostdump.Quote(c)+" = ?")
		args = append(args, vals[i])
	}
	// Identifiers from hostdump.Tables, quoted; every value is a bound parameter.
	res, err := g.conn.ExecContext(g.ctx, "UPDATE "+hostdump.Quote(t.Name)+" SET "+strings.Join(sets, ", ")+" WHERE "+strings.Join(where, " AND "), append(args, keyArgs...)...) //nolint:gosec // as above
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%s: the update touched %d rows", t.Name, n)
	}
	g.rep.Updated[t.Name]++
	return nil
}

func (g *merger) run(newRows map[string][]hostRow, newSeqs map[string]int64, m dbFacts) error {
	g.expected = map[string]map[string]string{}
	old := map[string]map[string]hostRow{}
	for _, t := range hostdump.Tables {
		_, byKey, err := readRows(g.ctx, g.conn, t)
		if err != nil {
			return fmt.Errorf("read old's %s: %w", t.Name, err)
		}
		old[t.Name] = byKey
	}

	// host_samples: insert what old lacks; each new row is past the manifest's newest sample.
	samples := tableNamed("host_samples")
	limit := m.maxTime(samples.Name)
	tsIdx := colIndex(samples, samples.TimeColumn)
	for _, row := range newRows[samples.Name] {
		if o, ok := old[samples.Name][row.key]; ok {
			if o.tag != row.tag {
				return conflict(samples, row.key, "new's row differs from old's")
			}
			continue
		}
		if ts, ok := intValue(row.vals[tsIdx]); limit != nil && (!ok || ts <= *limit) {
			return conflict(samples, row.key, fmt.Sprintf("old lacks it, and its ts %v is not after the manifest's newest sample %d", row.vals[tsIdx], *limit))
		}
		if err := g.insert(samples, row.vals); err != nil {
			return err
		}
	}

	// host_sample_rollups: the same rule, per host and grain: new rolls only complete buckets past
	// the watermark (hosts/rollups.go), so a bucket old lacks is past old's newest of that grain.
	rollups := tableNamed("host_sample_rollups")
	hostIdx, grainIdx, bucketIdx := colIndex(rollups, "host"), colIndex(rollups, "grain"), colIndex(rollups, "bucket_ts")
	newest := map[string]int64{}
	for _, o := range old[rollups.Name] {
		k := fmt.Sprint(o.vals[hostIdx], "\x00", o.vals[grainIdx])
		if b, ok := intValue(o.vals[bucketIdx]); ok && b > newest[k] {
			newest[k] = b
		}
	}
	for _, row := range newRows[rollups.Name] {
		if o, ok := old[rollups.Name][row.key]; ok {
			if o.tag != row.tag {
				return conflict(rollups, row.key, "new's bucket differs from old's: new never rewrites a rolled bucket")
			}
			continue
		}
		k := fmt.Sprint(row.vals[hostIdx], "\x00", row.vals[grainIdx])
		if b, ok := intValue(row.vals[bucketIdx]); !ok || (newest[k] != 0 && b <= newest[k]) {
			return conflict(rollups, row.key, fmt.Sprintf("old lacks it, and its bucket is not after old's newest %d of that host and grain", newest[k]))
		}
		if err := g.insert(rollups, row.vals); err != nil {
			return err
		}
	}

	// host_events: ids up to the manifest's sequence are old's own and must be equal; ids past it
	// are new's and inserted.
	events := tableNamed("host_events")
	seq, hasSeq := m.sequence()
	idIdx := colIndex(events, "id")
	var maxID int64
	for _, row := range newRows[events.Name] {
		id, _ := intValue(row.vals[idIdx])
		if id > maxID {
			maxID = id
		}
		o, ok := old[events.Name][row.key]
		switch {
		case ok && o.tag != row.tag:
			return conflict(events, row.key, "a shared id with different content")
		case ok:
		case hasSeq && id <= seq:
			return conflict(events, row.key, fmt.Sprintf("old lacks it, but its id is not past the manifest's sequence %d", seq))
		default:
			if err := g.insert(events, row.vals); err != nil {
				return err
			}
		}
	}
	var oldSeq int64
	if err := g.conn.QueryRowContext(g.ctx, "SELECT seq FROM sqlite_sequence WHERE name = ?", hostdump.SequenceTable).Scan(&oldSeq); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	want := max(oldSeq, newSeqs[hostdump.SequenceTable], maxID)
	if want != oldSeq {
		res, err := g.conn.ExecContext(g.ctx, "UPDATE sqlite_sequence SET seq = ? WHERE name = ?", want, hostdump.SequenceTable)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err = g.conn.ExecContext(g.ctx, "INSERT INTO sqlite_sequence (name, seq) VALUES (?, ?)", hostdump.SequenceTable, want); err != nil {
				return err
			}
		}
	}
	g.rep.SQLiteSequence = want

	// host_status: per host, new's row when its last_attempt is newer; a host new deleted keeps
	// old's row.
	status := tableNamed("host_status")
	g.expected[status.Name] = map[string]string{}
	laIdx := colIndex(status, "last_attempt")
	for _, row := range newRows[status.Name] {
		o, ok := old[status.Name][row.key]
		if !ok {
			g.expected[status.Name][row.key] = row.tag
			if err := g.insert(status, row.vals); err != nil {
				return err
			}
			continue
		}
		na, nok := intValue(row.vals[laIdx])
		oa, ook := intValue(o.vals[laIdx])
		switch {
		case o.tag == row.tag:
			g.expected[status.Name][row.key] = row.tag
		case nok && (!ook || na > oa):
			g.expected[status.Name][row.key] = row.tag
			if err := g.update(status, row.vals); err != nil {
				return err
			}
		case nok == ook && na == oa:
			return conflict(status, row.key, "the same last_attempt with different values")
		default:
			g.expected[status.Name][row.key] = o.tag
			g.rep.KeptOld[status.Name]++
		}
	}

	// host_rollup_state: the watermark is the larger of the two.
	state := tableNamed("host_rollup_state")
	g.expected[state.Name] = map[string]string{}
	wIdx := colIndex(state, "watermark")
	for _, row := range newRows[state.Name] {
		o, ok := old[state.Name][row.key]
		if !ok {
			g.expected[state.Name][row.key] = row.tag
			if err := g.insert(state, row.vals); err != nil {
				return err
			}
			continue
		}
		nw, _ := intValue(row.vals[wIdx])
		ow, _ := intValue(o.vals[wIdx])
		switch {
		case nw > ow:
			g.expected[state.Name][row.key] = row.tag
			if err := g.update(state, row.vals); err != nil {
				return err
			}
		default:
			g.expected[state.Name][row.key] = o.tag
			if nw < ow {
				g.rep.KeptOld[state.Name]++
			}
		}
	}
	return nil
}

// verify re-reads old inside the transaction: every row of new's three append tables is in
// old, status and rollup state are per key what the rules chose, the sequence is past every id,
// and integrity_check passes.
func (g *merger) verify(newRows map[string][]hostRow, newSeqs map[string]int64) error {
	for _, t := range hostdump.Tables {
		_, byKey, err := readRows(g.ctx, g.conn, t)
		if err != nil {
			return err
		}
		var missing int
		for _, row := range newRows[t.Name] {
			o, ok := byKey[row.key]
			switch {
			case g.expected[t.Name] != nil:
				if !ok || o.tag != g.expected[t.Name][row.key] {
					missing++
				}
			case !ok || o.tag != row.tag:
				missing++
			}
		}
		if missing > 0 {
			return fmt.Errorf("verify: %d of new's %s rows are not in old's main.db as the merge wrote them", missing, t.Name)
		}
	}
	var seq, maxID sql.NullInt64
	if err := g.conn.QueryRowContext(g.ctx, "SELECT (SELECT seq FROM sqlite_sequence WHERE name = ?), (SELECT MAX(id) FROM host_events)", hostdump.SequenceTable).Scan(&seq, &maxID); err != nil {
		return err
	}
	if maxID.Valid && (!seq.Valid || seq.Int64 < maxID.Int64 || seq.Int64 < newSeqs[hostdump.SequenceTable]) {
		return fmt.Errorf("verify: host_events sqlite_sequence %v is behind max(id) %d or new's %d", seq, maxID.Int64, newSeqs[hostdump.SequenceTable])
	}
	rows, err := g.conn.QueryContext(g.ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	var checks []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			_ = rows.Close()
			return err
		}
		checks = append(checks, s)
	}
	_ = rows.Close()
	g.rep.IntegrityCheck = strings.Join(checks, "; ")
	if g.rep.IntegrityCheck != "ok" {
		return fmt.Errorf("verify: old's main.db fails integrity_check: %s", g.rep.IntegrityCheck)
	}
	return nil
}

// dbFacts is what the merge needs of the manifest's main.db state: a table's newest time and
// host_events' sequence.
type dbFacts interface {
	maxTime(table string) *int64
	sequence() (int64, bool)
}

// manifestDB is what the merge needs of the manifest's main.db state.
type manifestDB struct{ s hostdump.State }

func (d manifestDB) maxTime(table string) *int64 { return d.s.Tables[table].MaxTime }

func (d manifestDB) sequence() (int64, bool) {
	v, ok := d.s.SQLiteSequence[hostdump.SequenceTable]
	return v, ok
}
