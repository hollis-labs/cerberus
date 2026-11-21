package hosts

import (
	"context"
	"time"
)

// SnapshotSamples returns the latest sample at or before now, and the last
// successful sample in each of twelve five-minute buckets ending at now.
// Selection happens in SQL: neither probe frequency nor history grows the result.
func (s *SampleStore) SnapshotSamples(ctx context.Context, host string, now time.Time) (*Sample, []Sample, error) {
	latest, err := s.Samples(ctx, host, time.Time{}, now, 1)
	if err != nil {
		return nil, nil, err
	}
	var last *Sample
	if len(latest) > 0 {
		last = &latest[0]
	}
	lo, hi := unix(now.Add(-time.Hour)), unix(now)
	rows, err := s.core.QueryContext(ctx, `SELECT `+sampleCols+` FROM host_samples
 WHERE host = ? AND ts IN (
 SELECT MAX(ts) FROM host_samples WHERE host = ? AND ts >= ? AND ts < ?
 GROUP BY (ts - ?) / 300) ORDER BY ts ASC LIMIT 12`, host, host, lo, hi, lo)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := []Sample{}
	for rows.Next() {
		x, err := scanSample(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, x)
	}
	return last, out, rows.Err()
}
