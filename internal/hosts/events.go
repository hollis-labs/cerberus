package hosts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/core/store"
)

// EventStatusChanged is the kind of event a status change appends.
const EventStatusChanged = "status_changed"

// Event is one entry of the append-only status event log. It carries the
// change and the reason codes only: nothing the host reported (serials,
// addresses, probed text), so it is safe to hand to any subscriber.
type Event struct {
	ID   int64     `json:"id"`
	At   time.Time `json:"ts"`
	Host string    `json:"host"`
	Kind string    `json:"kind"`
	From string    `json:"from"`
	To   string    `json:"to"`
	// Reasons are the codes of the new status's reasons (disk_low, unreachable, ...).
	Reasons []string `json:"reasons,omitempty"`
	// Expected marks a change on a host labelled intermittent=true (a laptop):
	// subscribers may mute it.
	Expected bool `json:"expected,omitempty"`
}

// EventQuery selects events after an id.
type EventQuery struct {
	AfterID int64
	Host    string
	Kind    string
	Limit   int
}

// AppendEvent adds an event and returns its id.
func (s *SampleStore) AppendEvent(ctx context.Context, e Event) (int64, error) {
	reasons, err := json.Marshal(e.Reasons)
	if err != nil {
		return 0, err
	}
	if e.Reasons == nil {
		reasons = []byte("[]")
	}
	exp := 0
	if e.Expected {
		exp = 1
	}
	var id int64
	err = s.core.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.QueryRowContext(ctx, `INSERT INTO host_events (ts, host, kind, from_status, to_status, reasons, expected)
			VALUES (?,?,?,?,?,?,?) RETURNING id`,
			unix(e.At), e.Host, e.Kind, e.From, e.To, string(reasons), exp).Scan(&id)
	})
	return id, err
}

// Events returns events with id greater than q.AfterID, oldest first, at most
// q.Limit (0 means 200).
func (s *SampleStore) Events(ctx context.Context, q EventQuery) ([]Event, error) {
	if q.Limit <= 0 {
		q.Limit = 200
	}
	query := `SELECT id, ts, host, kind, from_status, to_status, reasons, expected FROM host_events WHERE id > ?`
	args := []any{q.AfterID}
	if q.Host != "" {
		query += ` AND host = ?`
		args = append(args, q.Host)
	}
	if q.Kind != "" {
		query += ` AND kind = ?`
		args = append(args, q.Kind)
	}
	query += ` ORDER BY id ASC LIMIT ?`
	args = append(args, q.Limit)
	rows, err := s.core.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := []Event{}
	for rows.Next() {
		var e Event
		var ts int64
		var reasons string
		var exp int
		if err := rows.Scan(&e.ID, &ts, &e.Host, &e.Kind, &e.From, &e.To, &reasons, &exp); err != nil {
			return nil, err
		}
		e.At = time.Unix(ts, 0).UTC()
		_ = json.Unmarshal([]byte(reasons), &e.Reasons)
		e.Expected = exp == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventBounds returns the oldest retained event id and the newest; zero when
// the log is empty.
func (s *SampleStore) EventBounds(ctx context.Context) (oldest, newest int64, err error) {
	var lo, hi sql.NullInt64
	if err = s.core.QueryRowContext(ctx, `SELECT MIN(id), MAX(id) FROM host_events`).Scan(&lo, &hi); err != nil {
		return 0, 0, fmt.Errorf("read event bounds: %w", err)
	}
	return lo.Int64, hi.Int64, nil
}

// PruneEvents removes events older than before and returns how many.
func (s *SampleStore) PruneEvents(ctx context.Context, before time.Time) (int, error) {
	return s.pruneEvents(ctx, before)
}

func (s *SampleStore) pruneEvents(ctx context.Context, before time.Time) (int, error) {
	res, err := s.core.ExecContext(ctx, `DELETE FROM host_events WHERE ts < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
