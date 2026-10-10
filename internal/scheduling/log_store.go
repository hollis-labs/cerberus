package scheduling

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hollis-labs/libs/util/scheduler"
)

// RunLogs contains only sanitized output. A retained tail can be truncated;
// resource lifetime logs are never presented as a scheduled run's streams.
type RunLogs struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func (c *Core) migrateDelivery(ctx context.Context) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS cerberus_schedule_logs(fire_id TEXT NOT NULL,job_key TEXT NOT NULL,incarnation INTEGER NOT NULL,revision TEXT NOT NULL,claimed_at TEXT NOT NULL,stream TEXT NOT NULL,body TEXT NOT NULL DEFAULT '',truncated INTEGER NOT NULL DEFAULT 0,capture_error INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(fire_id,stream))`,
		`CREATE TABLE IF NOT EXISTS cerberus_schedule_outcomes(fire_id TEXT PRIMARY KEY,job_key TEXT NOT NULL,incarnation INTEGER NOT NULL,scheduled_at TEXT NOT NULL,claimed_at TEXT NOT NULL,state TEXT NOT NULL,observed INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS cerberus_schedule_episodes(job_key TEXT NOT NULL,incarnation INTEGER NOT NULL,episode_id TEXT NOT NULL DEFAULT '',failing INTEGER NOT NULL DEFAULT 0,misfires INTEGER NOT NULL DEFAULT 0,cursor TEXT NOT NULL DEFAULT '',PRIMARY KEY(job_key,incarnation))`,
		`CREATE TABLE IF NOT EXISTS cerberus_schedule_observations(event_id TEXT PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS cerberus_schedule_outbox(event_id TEXT PRIMARY KEY,job_key TEXT NOT NULL,incarnation INTEGER NOT NULL,episode_id TEXT NOT NULL,kind TEXT NOT NULL,fire_id TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending')`,
	} {
		if _, err := c.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
func (c *Core) beginLogs(ctx context.Context, j Job, f scheduler.Job) error {
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err = c.logClaim(ctx, tx, j, f); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cerberus_schedule_logs`).Scan(&count); err != nil {
		return err
	}
	if count >= 2048 {
		return errors.New("scheduled log metadata capacity exhausted")
	}
	revision, err := j.Revision()
	if err != nil {
		return err
	}
	for _, stream := range []string{"stdout", "stderr"} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO cerberus_schedule_logs(fire_id,job_key,incarnation,revision,claimed_at,stream) VALUES(?,?,?,?,?,?)`, f.FireID, j.Key(), j.Incarnation, revision, stamp(f.FiredAt), stream); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (c *Core) logClaim(ctx context.Context, tx *sql.Tx, j Job, f scheduler.Job) error {
	var n int
	revision, err := j.Revision()
	if err != nil {
		return err
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gosched_fires f JOIN gosched_schedules s ON s.id=f.schedule_id JOIN cerberus_schedule_receipts r ON r.fire_id=f.id WHERE f.id=? AND f.schedule_id=? AND f.status=? AND f.attempt=? AND f.fired_at=? AND f.claim_expires_at>? AND s.payload=? AND r.revision=? AND r.state IN ('prepared','sent')`, f.FireID, j.Key(), scheduler.FireClaimed, f.Attempt, stamp(f.FiredAt), stamp(c.now()), f.Payload, revision).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("log append has no current exact fire")
	}
	return nil
}
func (c *Core) appendLog(ctx context.Context, j Job, f scheduler.Job, stream, text string) error {
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err = c.logClaim(ctx, tx, j, f); err != nil {
		return err
	}
	var old string
	var truncated bool
	if err = tx.QueryRowContext(ctx, `SELECT body,truncated FROM cerberus_schedule_logs WHERE fire_id=? AND stream=?`, f.FireID, stream).Scan(&old, &truncated); err != nil {
		return err
	}
	per, total := c.logLimits()
	body := old + text
	if len(body) > per {
		body = body[len(body)-per:]
		truncated = true
	}
	var used int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(body AS BLOB))),0) FROM cerberus_schedule_logs`).Scan(&used); err != nil {
		return err
	}
	if used-len(old)+len(body) > total {
		body = old
		truncated = true
	}
	if _, err = tx.ExecContext(ctx, `UPDATE cerberus_schedule_logs SET body=?,truncated=? WHERE fire_id=? AND stream=?`, body, truncated, f.FireID, stream); err != nil {
		return err
	}
	return tx.Commit()
}

// Logs is called only after the service's read_sensitive authorization/audit.
// Selectors never grant access; a recreated job cannot read the old incarnation.
func (c *Core) Logs(ctx context.Context, owner, id, fireID string) (RunLogs, error) {
	out := RunLogs{}
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return out, err
	}
	defer done()
	var payload []byte
	var fireKey string
	if err = tx.QueryRowContext(ctx, `SELECT f.schedule_id,s.payload FROM gosched_fires f JOIN gosched_schedules s ON s.id=f.schedule_id WHERE f.id=?`, fireID).Scan(&fireKey, &payload); errors.Is(err, sql.ErrNoRows) {
		return out, Refusal("not_found", "run was not found for this job")
	} else if err != nil {
		return out, err
	}
	if fireKey != owner+"/"+id {
		return out, Refusal("not_found", "run was not found for this job")
	}
	job, err := decodeJob(payload)
	if err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT l.stream,l.body,l.truncated,l.capture_error FROM cerberus_schedule_logs l JOIN cerberus_schedule_receipts r ON r.fire_id=l.fire_id AND r.revision=l.revision AND r.job_key=l.job_key AND r.plan_hash<>'' WHERE l.fire_id=? AND l.job_key=? AND l.incarnation=?`, fireID, fireKey, job.Incarnation)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var stream, body string
		var truncated, captureError bool
		if err = rows.Scan(&stream, &body, &truncated, &captureError); err != nil {
			return out, err
		}
		if !captureError {
			count++
		}
		if stream == "stdout" {
			out.Stdout = body
		} else {
			out.Stderr = body
		}
		out.Truncated = out.Truncated || truncated
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	out.Available = count == 2
	if !out.Available {
		out.Reason = "per-fire stream capture is unavailable for this run"
	}
	return out, nil
}

// Cleanup is bounded and follows public Prune. There is deliberately no claim
// of a shared transaction: crash-window orphans are inaccessible and counted
// against the global cap, then reclaimed on reopen/explicit maintenance.
func (c *Core) cleanupLogs(ctx context.Context) error {
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return err
	}
	defer done()
	_, err = tx.ExecContext(ctx, `DELETE FROM cerberus_schedule_logs WHERE rowid IN (SELECT l.rowid FROM cerberus_schedule_logs l WHERE NOT EXISTS(SELECT 1 FROM gosched_fires f WHERE f.id=l.fire_id) LIMIT 128)`)
	if err != nil {
		return err
	}
	return tx.Commit()
}
