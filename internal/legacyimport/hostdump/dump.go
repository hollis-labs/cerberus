// Package hostdump is the old build's main.db as the migration and the D3 manifest see it: the
// five host tables, their columns and primary keys, a dialect-neutral digest of their rows, and
// the logical state the manifest records (docs/substrate-port-plan.md, section 5). migrate-from
// verifies its copy with the same digest the manifest records, so a manifest written of the live
// database before the window and the import's own check agree on what "the same rows" means.
//
// It reads only. Opening the old database is the caller's: immutable for a backup file (Open),
// plain read-only for a live one the manifest checks (OpenLive), which sees the un-checkpointed
// WAL and never checkpoints.
package hostdump

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // the old database is SQLite
)

// Table is one host table: its columns in a fixed order, the primary key it is ordered by, and the
// column its newest row is measured by.
type Table struct {
	Name    string
	Columns []string
	Key     []string
	// TimeColumn is the table's time column for max_time.
	TimeColumn string
}

// Tables are the five host tables in the fixed order the digest covers them. The columns are
// the old migrations 006 to 008, which the new schema keeps exactly (storage's
// TestHostTablesMatchTheOldSchema).
var Tables = []Table{
	{Name: "host_samples", Key: []string{"host", "ts"}, TimeColumn: "ts",
		Columns: []string{"host", "ts", "load1", "load5", "load15", "cpu", "mem_total", "mem_available", "disk_total", "disk_free", "uptime_s", "probe_ms"}},
	{Name: "host_status", Key: []string{"host"}, TimeColumn: "last_attempt",
		Columns: []string{"host", "status", "reasons", "since", "last_ok", "last_attempt", "next_due", "consecutive_failures", "candidate", "candidate_count"}},
	{Name: "host_sample_rollups", Key: []string{"host", "grain", "bucket_ts"}, TimeColumn: "bucket_ts",
		Columns: []string{"host", "grain", "bucket_ts", "n", "load1_n", "load1_sum", "load1_min", "load1_max", "mem_avail_sum", "mem_avail_min", "mem_avail_max",
			"disk_free_sum", "disk_free_min", "disk_free_max", "probe_ms_sum", "probe_ms_max", "uptime_max", "cpu_max", "mem_total_max", "disk_total_max"}},
	{Name: "host_rollup_state", Key: []string{"host", "grain"}, TimeColumn: "watermark",
		Columns: []string{"host", "grain", "watermark"}},
	{Name: "host_events", Key: []string{"id"}, TimeColumn: "ts",
		Columns: []string{"id", "ts", "host", "kind", "from_status", "to_status", "reasons", "expected"}},
}

// SequenceTable is the AUTOINCREMENT table whose sqlite_sequence value is carried.
const SequenceTable = "host_events"

// Querier is what the digest reads through: a *sql.DB, *sql.Conn or a store's reader.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Open opens a database file read-only and immutable: no lock, no WAL replay, no checkpoint, no
// -wal or -shm created or removed. It is for a backup (`sqlite3 main.db ".backup <copy>"`) that
// nothing writes. One connection, so an ATTACH on it holds.
func Open(path string) (*sql.DB, error) { return open(path, "mode=ro&immutable=1") }

// OpenLive opens a live database read-only (not immutable): it reads through the WAL that a
// running old build may not have checkpointed, and a read-only connection never checkpoints.
func OpenLive(path string) (*sql.DB, error) { return open(path, "mode=ro") }

func open(path, query string) (*sql.DB, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	db, err := sql.Open("sqlite", URI(path, query))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// URI is the file: URI for path with query.
func URI(path, query string) string {
	return (&url.URL{Scheme: "file", OmitHost: true, Path: path, RawQuery: query}).String()
}

// Quote quotes an SQL identifier.
func Quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// qualified is schema.table, or table without a schema.
func qualified(schema, table string) string {
	if schema == "" {
		return Quote(table)
	}
	return Quote(schema) + "." + Quote(table)
}

// SelectAll is the ordered SELECT of a table's columns.
func (t Table) SelectAll(schema string) string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = Quote(c)
	}
	keys := make([]string, len(t.Key))
	for i, c := range t.Key {
		keys[i] = Quote(c)
	}
	return "SELECT " + strings.Join(cols, ", ") + " FROM " + qualified(schema, t.Name) + " ORDER BY " + strings.Join(keys, ", ")
}

// ColumnList is the table's columns, quoted and comma-separated.
func (t Table) ColumnList() string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = Quote(c)
	}
	return strings.Join(cols, ", ")
}

// Tag is one value, type-tagged: i:<int64>, r:<float64 bits in hex>, t:<text>, b:<hex>, n.
// An integer and a real with the same value differ; so do NULL and the empty string.
func Tag(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "n", nil
	case int64:
		return "i:" + strconv.FormatInt(x, 10), nil
	case float64:
		return "r:" + strconv.FormatUint(math.Float64bits(x), 16), nil
	case string:
		return "t:" + x, nil
	case []byte:
		return "b:" + hex.EncodeToString(x), nil
	case bool:
		if x {
			return "i:1", nil
		}
		return "i:0", nil
	}
	return "", fmt.Errorf("hostdump: unexpected value type %T", v)
}

// Rows calls fn with every row of t, in key order, as scanned values.
func Rows(ctx context.Context, q Querier, schema string, t Table, fn func(vals []any) error) error {
	rows, err := q.QueryContext(ctx, t.SelectAll(schema))
	if err != nil {
		return fmt.Errorf("read %s: %w", t.Name, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		vals := make([]any, len(t.Columns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("read %s: %w", t.Name, err)
		}
		if err := fn(vals); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Digest is the sha256 over the five tables in the fixed order: per table its name, then each
// row in key order as a JSON array of tagged values, one line each. It is the "ordered dump"
// the manifest records, and it does not depend on page layout, WAL state or rowids.
func Digest(ctx context.Context, q Querier, schema string) (string, error) {
	h := sha256.New()
	for _, t := range Tables {
		fmt.Fprintf(h, "table %s\n", t.Name)
		err := Rows(ctx, q, schema, t, func(vals []any) error {
			tags := make([]string, len(vals))
			for i, v := range vals {
				s, err := Tag(v)
				if err != nil {
					return fmt.Errorf("%s: %w", t.Name, err)
				}
				tags[i] = s
			}
			line, err := json.Marshal(tags)
			if err != nil {
				return err
			}
			_, _ = h.Write(append(line, '\n'))
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// TableState is one table's logical state: its row count and its newest time (nil when empty).
type TableState struct {
	Count   int64  `json:"count"`
	MaxTime *int64 `json:"max_time"`
}

// State is the logical state of the host tables that the D3 manifest records and checks: a WAL
// checkpoint or a restart of the old build changes the file's bytes, never this.
type State struct {
	Tables map[string]TableState `json:"tables"`
	// HostEventsMaxID is max(id) of host_events, nil when it is empty.
	HostEventsMaxID *int64 `json:"host_events_max_id"`
	// SQLiteSequence is every sqlite_sequence row (name to seq).
	SQLiteSequence map[string]int64 `json:"sqlite_sequence"`
	// HostDumpSHA256 is Digest.
	HostDumpSHA256 string `json:"host_dump_sha256"`
}

// ReadState reads the logical state through q (schema "" is the main database).
func ReadState(ctx context.Context, q Querier, schema string) (State, error) {
	st := State{Tables: map[string]TableState{}, SQLiteSequence: map[string]int64{}}
	for _, t := range Tables {
		var ts TableState
		var maxTime sql.NullInt64
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*), MAX("+Quote(t.TimeColumn)+") FROM "+qualified(schema, t.Name)).Scan(&ts.Count, &maxTime); err != nil {
			return st, fmt.Errorf("count %s: %w", t.Name, err)
		}
		if maxTime.Valid {
			v := maxTime.Int64
			ts.MaxTime = &v
		}
		st.Tables[t.Name] = ts
	}
	var maxID sql.NullInt64
	if err := q.QueryRowContext(ctx, "SELECT MAX(id) FROM "+qualified(schema, SequenceTable)).Scan(&maxID); err != nil {
		return st, err
	}
	if maxID.Valid {
		v := maxID.Int64
		st.HostEventsMaxID = &v
	}
	seq, err := Sequences(ctx, q, schema)
	if err != nil {
		return st, err
	}
	st.SQLiteSequence = seq
	if st.HostDumpSHA256, err = Digest(ctx, q, schema); err != nil {
		return st, err
	}
	return st, nil
}

// Sequences reads sqlite_sequence (absent: none).
func Sequences(ctx context.Context, q Querier, schema string) (map[string]int64, error) {
	out := map[string]int64{}
	var n int
	master := "sqlite_master"
	if schema != "" {
		master = Quote(schema) + ".sqlite_master"
	}
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+master+" WHERE type = 'table' AND name = 'sqlite_sequence'").Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT name, seq FROM "+qualified(schema, "sqlite_sequence")+" ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		var seq int64
		if err := rows.Scan(&name, &seq); err != nil {
			return nil, err
		}
		out[name] = seq
	}
	return out, rows.Err()
}

// Diff names how b differs from a, or "" when they are equal.
func (a State) Diff(b State) string {
	var out []string
	for _, t := range Tables {
		x, y := a.Tables[t.Name], b.Tables[t.Name]
		if x.Count != y.Count {
			out = append(out, fmt.Sprintf("%s: %d rows, was %d", t.Name, y.Count, x.Count))
		}
		if !eqPtr(x.MaxTime, y.MaxTime) {
			out = append(out, fmt.Sprintf("%s: max(%s) %s, was %s", t.Name, t.TimeColumn, ptrString(y.MaxTime), ptrString(x.MaxTime)))
		}
	}
	if !eqPtr(a.HostEventsMaxID, b.HostEventsMaxID) {
		out = append(out, fmt.Sprintf("host_events: max(id) %s, was %s", ptrString(b.HostEventsMaxID), ptrString(a.HostEventsMaxID)))
	}
	for name, seq := range a.SQLiteSequence {
		if got, ok := b.SQLiteSequence[name]; !ok || got != seq {
			out = append(out, fmt.Sprintf("sqlite_sequence %s: %s, was %d", name, map[bool]string{true: strconv.FormatInt(got, 10), false: "absent"}[ok], seq))
		}
	}
	for name, seq := range b.SQLiteSequence {
		if _, ok := a.SQLiteSequence[name]; !ok {
			out = append(out, fmt.Sprintf("sqlite_sequence %s: %d, was absent", name, seq))
		}
	}
	if a.HostDumpSHA256 != b.HostDumpSHA256 {
		out = append(out, fmt.Sprintf("host rows digest %s, was %s", b.HostDumpSHA256, a.HostDumpSHA256))
	}
	return strings.Join(out, "; ")
}

func eqPtr(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func ptrString(p *int64) string {
	if p == nil {
		return "none"
	}
	return strconv.FormatInt(*p, 10)
}
