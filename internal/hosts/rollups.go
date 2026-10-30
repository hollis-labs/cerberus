package hosts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/core/store"
)

// Rollup grains, finest first. Each is built from the one before: 5m from raw
// samples, 1h from 5m, 1d from 1h.
const (
	Grain5m = "5m"
	Grain1h = "1h"
	Grain1d = "1d"
)

// Grains lists the rollup grains, finest first.
var Grains = []string{Grain5m, Grain1h, Grain1d}

func grainSeconds(g string) int64 {
	switch g {
	case Grain5m:
		return 300
	case Grain1h:
		return 3600
	case Grain1d:
		return 86400
	}
	return 0
}

// Retention is how long each tier is kept. The daily rollups are kept.
type Retention struct {
	Raw      time.Duration
	Rollup5m time.Duration
	Rollup1h time.Duration
	// Events is how long status events are kept (they are not rolled up).
	Events time.Duration
}

// Stat is the extremes and mean of a measure over one bucket.
type Stat struct {
	Min float64 `json:"min"`
	Avg float64 `json:"avg"`
	Max float64 `json:"max"`
}

// AvgMax is the mean and the maximum of a measure over one bucket.
type AvgMax struct {
	Avg float64 `json:"avg"`
	Max float64 `json:"max"`
}

// Rollup summarizes one bucket of a host's samples. A measure with no values in
// the bucket (a host that reports no load) is absent, not zero.
type Rollup struct {
	Host  string    `json:"host"`
	Grain string    `json:"grain"`
	At    time.Time `json:"ts"`
	// N is the number of samples in the bucket.
	N               int     `json:"n"`
	Load1           *Stat   `json:"load1,omitempty"`
	MemoryAvailable *Stat   `json:"memory_available,omitempty"`
	DiskFree        *Stat   `json:"disk_free,omitempty"`
	ProbeMS         *AvgMax `json:"probe_ms,omitempty"`
	UptimeSeconds   int64   `json:"uptime_seconds,omitempty"`
	CPU             int     `json:"cpu,omitempty"`
	MemoryTotal     int64   `json:"memory_total,omitempty"`
	DiskTotal       int64   `json:"disk_total,omitempty"`
}

// CompactReport is what one compaction did.
type CompactReport struct {
	RollupsWritten  int `json:"rollups_written"`
	RawDeleted      int `json:"raw_deleted"`
	Rollup5mDeleted int `json:"rollup_5m_deleted"`
	Rollup1hDeleted int `json:"rollup_1h_deleted"`
	EventsDeleted   int `json:"events_deleted"`
}

func (r CompactReport) String() string {
	return fmt.Sprintf("rollups_written=%d raw_deleted=%d rollup_5m_deleted=%d rollup_1h_deleted=%d events_deleted=%d", r.RollupsWritten, r.RawDeleted, r.Rollup5mDeleted, r.Rollup1hDeleted, r.EventsDeleted)
}

func (r CompactReport) Empty() bool { return r == CompactReport{} }

func (s *SampleStore) watermark(ctx context.Context, q store.Querier, host, grain string) (int64, bool, error) {
	var w int64
	err := q.QueryRowContext(ctx, `SELECT watermark FROM host_rollup_state WHERE host = ? AND grain = ?`, host, grain).Scan(&w)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return w, err == nil, err
}

// sourceTable says where a grain is built from, and the timestamp column.
func sourceFor(grain string) (table, tsCol string) {
	if grain == Grain5m {
		return "host_samples", "ts"
	}
	return "host_sample_rollups", "bucket_ts"
}

func prevGrain(grain string) string {
	switch grain {
	case Grain1h:
		return Grain5m
	case Grain1d:
		return Grain1h
	}
	return ""
}

// buildGrain rolls up every complete bucket of grain for host that is not yet
// rolled up, from the finer tier, in one transaction that also advances the
// watermark. A bucket is complete when the finer tier has been rolled up past
// its end (raw samples: when the bucket has ended). It returns the rows
// written. The transaction may be restarted on contention, so it recomputes
// everything from the table each time.
func (s *SampleStore) buildGrain(ctx context.Context, host, grain string, now time.Time) (int, error) {
	size := grainSeconds(grain)
	written := 0
	err := s.core.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		written = 0
		// The end, exclusive, of what the source tier is complete for.
		var limit int64
		if finer := prevGrain(grain); finer == "" {
			limit = unix(now) / size * size
		} else {
			w, ok, err := s.watermark(ctx, tx, host, finer)
			if err != nil || !ok {
				return err
			}
			limit = w / size * size
		}
		from, ok, err := s.watermark(ctx, tx, host, grain)
		if err != nil {
			return err
		}
		table, tsCol := sourceFor(grain)
		if !ok {
			var first sql.NullInt64
			q := fmt.Sprintf(`SELECT MIN(%s) FROM %s WHERE host = ?`, tsCol, table)
			if grain != Grain5m {
				q += fmt.Sprintf(` AND grain = '%s'`, prevGrain(grain))
			}
			if err := tx.QueryRowContext(ctx, q, host).Scan(&first); err != nil {
				return err
			}
			if !first.Valid {
				return nil
			}
			from = first.Int64 / size * size
		}
		if limit <= from {
			return nil
		}

		const upsert = ` ON CONFLICT(host, grain, bucket_ts) DO UPDATE SET
			n = excluded.n, load1_n = excluded.load1_n, load1_sum = excluded.load1_sum, load1_min = excluded.load1_min, load1_max = excluded.load1_max,
			mem_avail_sum = excluded.mem_avail_sum, mem_avail_min = excluded.mem_avail_min, mem_avail_max = excluded.mem_avail_max,
			disk_free_sum = excluded.disk_free_sum, disk_free_min = excluded.disk_free_min, disk_free_max = excluded.disk_free_max,
			probe_ms_sum = excluded.probe_ms_sum, probe_ms_max = excluded.probe_ms_max, uptime_max = excluded.uptime_max,
			cpu_max = excluded.cpu_max, mem_total_max = excluded.mem_total_max, disk_total_max = excluded.disk_total_max`
		const cols = `(host, grain, bucket_ts, n, load1_n, load1_sum, load1_min, load1_max, mem_avail_sum, mem_avail_min, mem_avail_max,
			 disk_free_sum, disk_free_min, disk_free_max, probe_ms_sum, probe_ms_max, uptime_max, cpu_max, mem_total_max, disk_total_max)`
		var res sql.Result
		if grain == Grain5m {
			res, err = tx.ExecContext(ctx, `INSERT INTO host_sample_rollups `+cols+`
				SELECT host, ?, (ts / ?) * ?, COUNT(*), COUNT(load1), SUM(load1), MIN(load1), MAX(load1),
				       SUM(mem_available), MIN(mem_available), MAX(mem_available),
				       SUM(disk_free), MIN(disk_free), MAX(disk_free), SUM(probe_ms), MAX(probe_ms),
				       MAX(uptime_s), MAX(cpu), MAX(mem_total), MAX(disk_total)
				FROM host_samples WHERE host = ? AND ts >= ? AND ts < ? GROUP BY host, (ts / ?) * ?`+upsert,
				grain, size, size, host, from, limit, size, size)
		} else {
			res, err = tx.ExecContext(ctx, `INSERT INTO host_sample_rollups `+cols+`
				SELECT host, ?, (bucket_ts / ?) * ?, SUM(n), SUM(load1_n), SUM(load1_sum), MIN(load1_min), MAX(load1_max),
				       SUM(mem_avail_sum), MIN(mem_avail_min), MAX(mem_avail_max),
				       SUM(disk_free_sum), MIN(disk_free_min), MAX(disk_free_max), SUM(probe_ms_sum), MAX(probe_ms_max),
				       MAX(uptime_max), MAX(cpu_max), MAX(mem_total_max), MAX(disk_total_max)
				FROM host_sample_rollups WHERE host = ? AND grain = ? AND bucket_ts >= ? AND bucket_ts < ? GROUP BY host, (bucket_ts / ?) * ?`+upsert,
				grain, size, size, host, prevGrain(grain), from, limit, size, size)
		}
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		written = int(n)
		_, err = tx.ExecContext(ctx, `INSERT INTO host_rollup_state (host, grain, watermark) VALUES (?,?,?)
			ON CONFLICT(host, grain) DO UPDATE SET watermark = excluded.watermark`, host, grain, limit)
		return err
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

// hostsWithSamples lists every host that has samples or rollups.
func (s *SampleStore) hostsWithSamples(ctx context.Context) ([]string, error) {
	rows, err := s.core.QueryContext(ctx, `SELECT host FROM host_samples UNION SELECT host FROM host_sample_rollups`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// deleteBelow removes rows older than cutoff, but only below the watermark the
// next tier has committed: nothing is removed that a committed rollup does not
// count. It returns the rows removed.
func (s *SampleStore) deleteBelow(ctx context.Context, host, table, tsCol, grainFilter, nextGrain string, cutoff time.Time) (int, error) {
	removed := 0
	err := s.core.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		removed = 0
		w, ok, err := s.watermark(ctx, tx, host, nextGrain)
		if err != nil || !ok {
			return err
		}
		limit := unix(cutoff)
		if w < limit {
			limit = w
		}
		q := fmt.Sprintf(`DELETE FROM %s WHERE host = ? AND %s < ?`, table, tsCol)
		args := []any{host, limit}
		if grainFilter != "" {
			q += ` AND grain = ?`
			args = append(args, grainFilter)
		}
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		removed = int(n)
		return nil
	})
	return removed, err
}

// Compact builds every complete rollup bucket that is missing and then
// removes what retention allows: raw samples older than ret.Raw, 5m rollups
// older than ret.Rollup5m, 1h rollups older than ret.Rollup1h, each only below
// the watermark of the tier that now counts it. It is idempotent, and a failure
// leaves earlier tiers' data in place. Daily rollups are kept.
func (s *SampleStore) Compact(ctx context.Context, now time.Time, ret Retention) (CompactReport, error) {
	var rep CompactReport
	hs, err := s.hostsWithSamples(ctx)
	if err != nil {
		return rep, err
	}
	for _, h := range hs {
		for _, g := range Grains {
			n, err := s.buildGrain(ctx, h, g, now)
			if err != nil {
				return rep, fmt.Errorf("roll up %s %s: %w", h, g, err)
			}
			rep.RollupsWritten += n
		}
		for _, d := range []struct {
			table, tsCol, grain, next string
			age                       time.Duration
			count                     *int
		}{
			{"host_samples", "ts", "", Grain5m, ret.Raw, &rep.RawDeleted},
			{"host_sample_rollups", "bucket_ts", Grain5m, Grain1h, ret.Rollup5m, &rep.Rollup5mDeleted},
			{"host_sample_rollups", "bucket_ts", Grain1h, Grain1d, ret.Rollup1h, &rep.Rollup1hDeleted},
		} {
			if d.age <= 0 {
				continue
			}
			n, err := s.deleteBelow(ctx, h, d.table, d.tsCol, d.grain, d.next, now.Add(-d.age))
			if err != nil {
				return rep, fmt.Errorf("retention %s %s: %w", h, d.table, err)
			}
			*d.count += n
		}
	}
	if ret.Events > 0 {
		n, err := s.pruneEvents(ctx, now.Add(-ret.Events))
		if err != nil {
			return rep, fmt.Errorf("retention events: %w", err)
		}
		rep.EventsDeleted = n
	}
	return rep, nil
}

const rollupCols = `host, grain, bucket_ts, n, load1_n, load1_sum, load1_min, load1_max, mem_avail_sum, mem_avail_min, mem_avail_max,
	disk_free_sum, disk_free_min, disk_free_max, probe_ms_sum, probe_ms_max, uptime_max, cpu_max, mem_total_max, disk_total_max`

// Rollups returns a host's rollups of one grain in time order between since and
// until (zero is unbounded), at most limit (0 means 500, the newest).
func (s *SampleStore) Rollups(ctx context.Context, host, grain string, since, until time.Time, limit int) ([]Rollup, error) {
	if grainSeconds(grain) == 0 {
		return nil, fmt.Errorf("unknown grain %q: use raw, 5m, 1h or 1d", grain)
	}
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
	rows, err := s.core.QueryContext(ctx, `SELECT `+rollupCols+` FROM (
		SELECT `+rollupCols+` FROM host_sample_rollups WHERE host = ? AND grain = ? AND bucket_ts >= ? AND bucket_ts <= ?
		ORDER BY bucket_ts DESC LIMIT ?) ORDER BY bucket_ts ASC`, host, grain, lo, hi, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := []Rollup{}
	for rows.Next() {
		var r Rollup
		var ts int64
		var l1n int
		var l1s, l1min, l1max sql.NullFloat64
		var mas, mamin, mamax, dfs, dfmin, dfmax, pms, pmmax, up, cpu, mt, dt sql.NullInt64
		if err := rows.Scan(&r.Host, &r.Grain, &ts, &r.N, &l1n, &l1s, &l1min, &l1max, &mas, &mamin, &mamax, &dfs, &dfmin, &dfmax, &pms, &pmmax, &up, &cpu, &mt, &dt); err != nil {
			return nil, err
		}
		r.At = time.Unix(ts, 0).UTC()
		if l1n > 0 && l1s.Valid {
			r.Load1 = &Stat{Min: l1min.Float64, Avg: l1s.Float64 / float64(l1n), Max: l1max.Float64}
		}
		if mas.Valid && r.N > 0 {
			r.MemoryAvailable = &Stat{Min: float64(mamin.Int64), Avg: float64(mas.Int64) / float64(r.N), Max: float64(mamax.Int64)}
		}
		if dfs.Valid && r.N > 0 {
			r.DiskFree = &Stat{Min: float64(dfmin.Int64), Avg: float64(dfs.Int64) / float64(r.N), Max: float64(dfmax.Int64)}
		}
		if pms.Valid && r.N > 0 {
			r.ProbeMS = &AvgMax{Avg: float64(pms.Int64) / float64(r.N), Max: float64(pmmax.Int64)}
		}
		r.UptimeSeconds, r.CPU, r.MemoryTotal, r.DiskTotal = up.Int64, int(cpu.Int64), mt.Int64, dt.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}
