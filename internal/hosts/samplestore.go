package hosts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/core/store"
)

// SampleStore keeps samples and derived status in the daemon's SQLite
// database (tables host_samples and host_status, migration 006).
//
// It goes through libs core/store: reads use the read pool, and every write is
// a WriteTx on the single writer, which waits for the lock and restarts on
// contention, so monitor goroutines for different hosts never lose a write.
type SampleStore struct {
	core *store.Store
}

// ErrLateSample is returned for a sample older than the host's rolled-up
// watermark.
var ErrLateSample = errors.New("late sample")

// NewSampleStore wraps an open core/store handle whose database has the
// migrations applied.
func NewSampleStore(core *store.Store) *SampleStore { return &SampleStore{core: core} }

func nullF(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func unix(t time.Time) int64 { return t.UTC().Unix() }

func unixPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return unix(*t)
}

// AppendSample records a measurement. A second sample in the same second for a
// host replaces the first.
func (s *SampleStore) AppendSample(ctx context.Context, x Sample) error {
	return s.core.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		// A sample behind the host's 5-minute watermark belongs to a bucket that
		// has already been rolled up (a host clock that stepped backwards, say).
		// It would be left in the raw table uncounted and then deleted by
		// retention, so it is refused rather than stored. The check and the
		// insert are one transaction.
		w, ok, err := s.watermark(ctx, tx, x.Host, Grain5m)
		if err != nil {
			return err
		}
		if ok && unix(x.At) < w {
			return fmt.Errorf("%w: sample at %s is before the rolled-up watermark %s", ErrLateSample, x.At.UTC().Format(time.RFC3339), time.Unix(w, 0).UTC().Format(time.RFC3339))
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO host_samples
			(host, ts, load1, load5, load15, cpu, mem_total, mem_available, disk_total, disk_free, uptime_s, probe_ms)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(host, ts) DO UPDATE SET load1 = excluded.load1, load5 = excluded.load5, load15 = excluded.load15,
			  cpu = excluded.cpu, mem_total = excluded.mem_total, mem_available = excluded.mem_available,
			  disk_total = excluded.disk_total, disk_free = excluded.disk_free, uptime_s = excluded.uptime_s, probe_ms = excluded.probe_ms`,
			x.Host, unix(x.At), nullF(x.Load1), nullF(x.Load5), nullF(x.Load15), x.CPU, x.MemoryTotal, x.MemoryAvailable,
			x.DiskTotal, x.DiskFree, x.UptimeSeconds, x.ProbeMS)
		return err
	})
}

func scanSample(rows *sql.Rows) (Sample, error) {
	var x Sample
	var ts int64
	var l1, l5, l15 sql.NullFloat64
	var cpu, mt, ma, dt, df, up, pm sql.NullInt64
	if err := rows.Scan(&x.Host, &ts, &l1, &l5, &l15, &cpu, &mt, &ma, &dt, &df, &up, &pm); err != nil {
		return x, err
	}
	x.At = time.Unix(ts, 0).UTC()
	for src, dst := range map[*sql.NullFloat64]**float64{&l1: &x.Load1, &l5: &x.Load5, &l15: &x.Load15} {
		if src.Valid {
			v := src.Float64
			*dst = &v
		}
	}
	x.CPU, x.MemoryTotal, x.MemoryAvailable = int(cpu.Int64), mt.Int64, ma.Int64
	x.DiskTotal, x.DiskFree, x.UptimeSeconds, x.ProbeMS = dt.Int64, df.Int64, up.Int64, pm.Int64
	return x, nil
}

const sampleCols = `host, ts, load1, load5, load15, cpu, mem_total, mem_available, disk_total, disk_free, uptime_s, probe_ms`

// Recent returns a host's latest n samples, newest first.
func (s *SampleStore) Recent(ctx context.Context, host string, n int) ([]Sample, error) {
	rows, err := s.core.QueryContext(ctx, `SELECT `+sampleCols+` FROM host_samples WHERE host = ? ORDER BY ts DESC LIMIT ?`, host, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var out []Sample
	for rows.Next() {
		x, err := scanSample(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Samples returns a host's samples in time order between since and until
// (zero means unbounded), at most limit (0 means 500). When more match, the
// newest limit are returned.
func (s *SampleStore) Samples(ctx context.Context, host string, since, until time.Time, limit int) ([]Sample, error) {
	if limit <= 0 {
		limit = 500
	}
	lo, hi := int64(0), int64(1<<62)
	if !since.IsZero() {
		lo = unix(since)
	}
	if !until.IsZero() {
		hi = unix(until)
	}
	rows, err := s.core.QueryContext(ctx, `SELECT `+sampleCols+` FROM (
		SELECT `+sampleCols+` FROM host_samples WHERE host = ? AND ts >= ? AND ts <= ? ORDER BY ts DESC LIMIT ?) ORDER BY ts ASC`, host, lo, hi, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := []Sample{}
	for rows.Next() {
		x, err := scanSample(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// PutStatus stores a host's status.
func (s *SampleStore) PutStatus(ctx context.Context, st HostStatus) error {
	reasons, err := json.Marshal(st.Reasons)
	if err != nil {
		return err
	}
	if st.Reasons == nil {
		reasons = []byte("[]")
	}
	return s.core.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO host_status
			(host, status, reasons, since, last_ok, last_attempt, next_due, consecutive_failures, candidate, candidate_count)
			VALUES (?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(host) DO UPDATE SET status = excluded.status, reasons = excluded.reasons, since = excluded.since,
			  last_ok = excluded.last_ok, last_attempt = excluded.last_attempt, next_due = excluded.next_due,
			  consecutive_failures = excluded.consecutive_failures, candidate = excluded.candidate, candidate_count = excluded.candidate_count`,
			st.Host, st.Status, string(reasons), unix(st.Since), unixPtr(st.LastOK), unixPtr(st.LastAttempt), unixPtr(st.NextDue),
			st.ConsecutiveFailures, st.Candidate, st.CandidateCount)
		return err
	})
}

func scanStatus(sc interface{ Scan(...any) error }) (HostStatus, error) {
	var st HostStatus
	var reasons string
	var since int64
	var lastOK, lastAttempt, nextDue sql.NullInt64
	if err := sc.Scan(&st.Host, &st.Status, &reasons, &since, &lastOK, &lastAttempt, &nextDue, &st.ConsecutiveFailures, &st.Candidate, &st.CandidateCount); err != nil {
		return st, err
	}
	st.Since = time.Unix(since, 0).UTC()
	_ = json.Unmarshal([]byte(reasons), &st.Reasons)
	for src, dst := range map[*sql.NullInt64]**time.Time{&lastOK: &st.LastOK, &lastAttempt: &st.LastAttempt, &nextDue: &st.NextDue} {
		if src.Valid {
			t := time.Unix(src.Int64, 0).UTC()
			*dst = &t
		}
	}
	return st, nil
}

const statusCols = `host, status, reasons, since, last_ok, last_attempt, next_due, consecutive_failures, candidate, candidate_count`

// Status returns a host's status; ok is false when none was recorded.
func (s *SampleStore) Status(ctx context.Context, host string) (HostStatus, bool, error) {
	st, err := scanStatus(s.core.QueryRowContext(ctx, `SELECT `+statusCols+` FROM host_status WHERE host = ?`, host))
	if errors.Is(err, sql.ErrNoRows) {
		return HostStatus{}, false, nil
	}
	if err != nil {
		return HostStatus{}, false, fmt.Errorf("read host status: %w", err)
	}
	return st, true, nil
}

// AllStatus returns every host's status, by host name.
func (s *SampleStore) AllStatus(ctx context.Context) (map[string]HostStatus, error) {
	rows, err := s.core.QueryContext(ctx, `SELECT `+statusCols+` FROM host_status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := map[string]HostStatus{}
	for rows.Next() {
		st, err := scanStatus(rows)
		if err != nil {
			return nil, err
		}
		out[st.Host] = st
	}
	return out, rows.Err()
}

// ForgetStatus drops the status of hosts not in keep. Samples are history and
// stay.
func (s *SampleStore) ForgetStatus(ctx context.Context, keep map[string]bool) error {
	all, err := s.AllStatus(ctx)
	if err != nil {
		return err
	}
	return s.core.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		for host := range all {
			if !keep[host] {
				if _, err := tx.ExecContext(ctx, `DELETE FROM host_status WHERE host = ?`, host); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
