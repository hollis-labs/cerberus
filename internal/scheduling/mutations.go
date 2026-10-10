package scheduling

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hollis-labs/libs/util/scheduler/sqlstore"
	"time"

	"github.com/hollis-labs/libs/util/scheduler"
)

const sqlTime = "2006-01-02T15:04:05.000000000Z"

func stamp(t time.Time) string { return t.UTC().Format(sqlTime) }

// The library has no transaction-aware update/create API. This extension
// updates its published v0.4.0 SQL schema atomically, leaving its recurrence,
// claim, overlap, receipt and retention algorithms in the library/core.
func (c *Core) writeTx(ctx context.Context) (*sql.Tx, func(), error) {
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	// The library reapplies its busy timeout on each write connection. Our
	// transaction extension must do the same for newly grown caller pools.
	if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", sqlstore.DefaultBusyTimeout.Milliseconds())); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	done := func() { _ = tx.Rollback(); _ = conn.Close() }
	if _, err = tx.ExecContext(ctx, `UPDATE gosched_schedules SET next_run=next_run WHERE 0`); err != nil {
		done()
		return nil, nil, err
	}
	return tx, done, nil
}

func nextGeneration(ctx context.Context, tx *sql.Tx, key string) (uint64, error) {
	var n uint64
	err := tx.QueryRowContext(ctx, `INSERT INTO cerberus_schedule_generations(job_key,generation) VALUES(?,1) ON CONFLICT(job_key) DO UPDATE SET generation=generation+1 RETURNING generation`, key).Scan(&n)
	return n, err
}
func scheduleOptions(s scheduler.Schedule) ([]byte, error) {
	// Exact names are the published sqlstore schedule-options contract.
	return json.Marshal(struct {
		Location                   string
		Interval                   time.Duration
		KeepLastN                  int
		Overlap                    scheduler.OverlapPolicy
		Misfire                    scheduler.MisfirePolicy
		MisfireGrace               time.Duration
		MaxCatchUp, MaxQueuedFires int
	}{s.Location, s.Interval, s.KeepLastN, s.Overlap, s.Misfire, s.MisfireGrace, s.MaxCatchUp, s.MaxQueuedFires})
}
func insertSchedule(ctx context.Context, tx *sql.Tx, s scheduler.Schedule) error {
	retry, err := json.Marshal(s.Retry)
	if err != nil {
		return err
	}
	options, err := scheduleOptions(s)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO gosched_schedules(id,cron_expr,last_run,next_run,enabled,job_type,payload,retry_json) VALUES(?,?,?,?,?,?,?,?)`, s.ID, s.CronExpr, stamp(s.LastRun), stamp(s.NextRun), s.Enabled, s.JobType, s.Payload, string(retry)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO gosched_schedule_options(schedule_id,options_json) VALUES(?,?)`, s.ID, string(options))
	return err
}
func (c *Core) createJob(ctx context.Context, j Job, key string) (JobView, error) {
	if _, err := j.schedule(c.now(), c.maxTimeout); err != nil {
		return JobView{}, Refusal("invalid", err.Error())
	}
	fingerprint, err := j.Revision()
	if err != nil {
		return JobView{}, err
	}
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return JobView{}, err
	}
	defer done()
	var previous, jobKey string
	var incarnation uint64
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,job_key,incarnation FROM cerberus_schedule_creates WHERE owner_app=? AND idempotency_key=?`, j.OwnerApp, key).Scan(&previous, &jobKey, &incarnation)
	if err == nil {
		if previous != fingerprint || jobKey != j.Key() {
			return JobView{}, Refusal("conflict", "idempotency key was used for a different create")
		}
		done()
		v, e := c.view(ctx, j.OwnerApp, j.ID)
		if ErrorCode(e) == "not_found" || e == nil && v.Job.Incarnation != incarnation {
			return v, Refusal("conflict", "idempotent create was deleted; it will not be replayed")
		}
		return v, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return JobView{}, err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gosched_schedules WHERE id=?`, j.Key()).Scan(&exists); err != nil {
		return JobView{}, err
	}
	if exists != 0 {
		return JobView{}, Refusal("conflict", "job already exists")
	}
	j.Generation, err = nextGeneration(ctx, tx, j.Key())
	if err != nil {
		return JobView{}, err
	}
	j.Incarnation = j.Generation
	s, err := j.schedule(c.now(), c.maxTimeout)
	if err != nil {
		return JobView{}, err
	}
	if err = insertSchedule(ctx, tx, s); err != nil {
		return JobView{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO cerberus_schedule_creates(owner_app,idempotency_key,fingerprint,job_key,incarnation) VALUES(?,?,?,?,?)`, j.OwnerApp, key, fingerprint, j.Key(), j.Incarnation); err != nil {
		return JobView{}, err
	}
	if err = tx.Commit(); err != nil {
		return JobView{}, err
	}
	return viewOf(j, s)
}
func (c *Core) editJob(ctx context.Context, r Call) (JobView, error) {
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return JobView{}, err
	}
	defer done()
	var payload []byte
	var nextRun string
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT payload,next_run,enabled FROM gosched_schedules WHERE id=?`, r.OwnerApp+"/"+r.ID).Scan(&payload, &nextRun, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return JobView{}, Refusal("not_found", "scheduled job was not found")
	}
	if err != nil {
		return JobView{}, err
	}
	j, err := decodeJob(payload)
	if err != nil {
		return JobView{}, err
	}
	rev, err := j.Revision()
	if err != nil {
		return JobView{}, err
	}
	if rev != r.Revision {
		return JobView{}, Refusal("conflict", "job revision changed; read it again before editing")
	}
	if r.Operation == "delete" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM gosched_schedules WHERE id=?`, j.Key()); err != nil {
			return JobView{}, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM gosched_schedule_options WHERE schedule_id=?`, j.Key()); err != nil {
			return JobView{}, err
		}
		return JobView{}, tx.Commit()
	}
	if r.Operation == "update" {
		incarnation := j.Incarnation
		j = *r.Job
		j.Incarnation = incarnation
	} else {
		j.Enabled = r.Operation == "resume"
	}
	j.Generation, err = nextGeneration(ctx, tx, j.Key())
	if err != nil {
		return JobView{}, err
	}
	s, err := j.schedule(c.now(), c.maxTimeout)
	if err != nil {
		return JobView{}, Refusal("invalid", err.Error())
	}
	// Pause keeps the due cursor. Resume advances recurrence from now rather
	// than replaying backlog. Neither resets fires, receipts or prune fences.
	if r.Operation == "pause" {
		s.NextRun, err = time.Parse(sqlTime, nextRun)
		if err != nil {
			return JobView{}, err
		}
	}
	options, err := scheduleOptions(s)
	if err != nil {
		return JobView{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gosched_schedules SET cron_expr=?,next_run=?,enabled=?,payload=? WHERE id=?`, s.CronExpr, stamp(s.NextRun), s.Enabled, s.Payload, s.ID); err != nil {
		return JobView{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO gosched_schedule_options(schedule_id,options_json) VALUES(?,?) ON CONFLICT(schedule_id) DO UPDATE SET options_json=excluded.options_json`, s.ID, string(options)); err != nil {
		return JobView{}, err
	}
	if err = tx.Commit(); err != nil {
		return JobView{}, err
	}
	return viewOf(j, s)
}
func (c *Core) runNow(ctx context.Context, r Call) (RunView, error) {
	// Refuse before durable fire creation when no trusted effect authority exists.
	if c.authority == nil || c.executor == nil {
		return RunView{}, Refusal("forbidden", "run-now requires current per-fire authority and a bound executor; nothing was sent")
	}
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return RunView{}, err
	}
	defer done()
	key := r.OwnerApp + "/" + r.ID
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT fire_id FROM cerberus_schedule_manual WHERE job_key=? AND request_id=?`, key, r.RequestID).Scan(&existingID)
	if err == nil {
		done()
		f, found, e := c.Fire(ctx, existingID)
		if e != nil {
			return RunView{}, e
		}
		if !found {
			return RunView{}, Refusal("conflict", "run history was pruned; this request will not be replayed")
		}
		return c.runView(ctx, f)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RunView{}, err
	}
	var payload []byte
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT payload,enabled FROM gosched_schedules WHERE id=?`, key).Scan(&payload, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return RunView{}, Refusal("not_found", "scheduled job was not found")
	}
	if err != nil {
		return RunView{}, err
	}
	j, err := decodeJob(payload)
	if err != nil {
		return RunView{}, err
	}
	if !enabled || !j.Enabled {
		return RunView{}, Refusal("conflict", "job is paused or completed")
	}
	at := c.now().UTC()
	var through string
	fenceErr := tx.QueryRowContext(ctx, `SELECT through_at FROM gosched_pruned WHERE schedule_id=?`, key).Scan(&through)
	if fenceErr != nil && !errors.Is(fenceErr, sql.ErrNoRows) {
		return RunView{}, fenceErr
	}
	if fenceErr == nil && stamp(at) <= through {
		return RunView{}, Refusal("conflict", "this occurrence is behind the permanent prune fence; nothing was sent")
	}
	if j.Overlap == scheduler.OverlapQueue {
		limit := j.MaxQueuedFires
		if limit == 0 {
			limit = scheduler.DefaultMaxQueuedFires
		}
		var pending int
		if queryErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gosched_fires WHERE schedule_id=? AND status IN ('pending','retrying')`, key).Scan(&pending); queryErr != nil {
			return RunView{}, queryErr
		}
		if pending >= limit {
			return RunView{}, Refusal("conflict", "the schedule queue is full; nothing was sent")
		}
	}
	fireID := scheduler.DeriveFireID(key, at)
	retry, _ := json.Marshal(scheduler.RetryPolicy{MaxAttempts: 1})
	// A manual fire leaves recurrence cursors untouched and uses the library's
	// deterministic occurrence ID, ordinary overlap claim and one-attempt path.
	result, err := tx.ExecContext(ctx, `INSERT INTO gosched_fires(id,schedule_id,scheduled_at,fired_at,claim_expires_at,attempt,status,next_attempt_at,last_error,retry_json,job_type,payload) VALUES(?,?,?,?,?,0,?,?, '',?,?,?) ON CONFLICT(id) DO NOTHING`, fireID, key, stamp(at), stamp(time.Time{}), stamp(time.Time{}), scheduler.FirePending, stamp(at), string(retry), jobType, payload)
	if err != nil {
		return RunView{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return RunView{}, err
	}
	if n != 1 {
		return RunView{}, Refusal("conflict", "another fire already occupies this instant; nothing was sent")
	}
	opts, err := json.Marshal(struct {
		Overlap        scheduler.OverlapPolicy
		MaxQueuedFires int
		Reason         string
	}{j.Overlap, j.MaxQueuedFires, "manual"})
	if err != nil {
		return RunView{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO gosched_fire_options(fire_id,options_json) VALUES(?,?)`, fireID, string(opts)); err != nil {
		return RunView{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO cerberus_schedule_manual(job_key,request_id,fire_id) VALUES(?,?,?)`, key, r.RequestID, fireID); err != nil {
		return RunView{}, err
	}
	if err = tx.Commit(); err != nil {
		return RunView{}, err
	}
	done()
	f, claimed, err := c.store.ClaimFire(ctx, scheduler.FireClaim{FireID: fireID, ExpectedStatus: scheduler.FirePending, ClaimedAt: at, ClaimExpiresAt: at.Add(c.maxTimeout)})
	if err != nil {
		if errors.Is(err, scheduler.ErrScheduleBusy) {
			// Leave the durable pending fire for the ordinary overlap dispatcher.
			pending, found, readErr := c.Fire(context.WithoutCancel(ctx), fireID)
			if readErr != nil {
				return RunView{}, readErr
			}
			if !found {
				return RunView{}, Refusal("not_found", "manual fire was not found")
			}
			return c.runView(context.WithoutCancel(ctx), pending)
		}
		return RunView{}, err
	}
	if !claimed {
		f, _, err = c.Fire(ctx, fireID)
		if err != nil {
			return RunView{}, err
		}
		return c.runView(ctx, f)
	}
	runCtx, cancel := context.WithTimeout(ctx, c.maxTimeout)
	defer cancel()
	runErr := c.Enqueue(runCtx, scheduler.Job{ScheduleID: key, FireID: fireID, JobType: jobType, Payload: bytes.Clone(payload), Attempt: f.Attempt, ScheduledAt: at, FiredAt: f.FiredAt})
	status := scheduler.FireSucceeded
	text := ""
	if runErr != nil {
		status = scheduler.FireExhausted
		text = runErr.Error()
	}
	_, err = c.store.TransitionFire(context.WithoutCancel(ctx), scheduler.FireTransition{FireID: fireID, Attempt: f.Attempt, From: scheduler.FireClaimed, ClaimedAt: f.FiredAt, To: status, At: c.now(), Error: text})
	if err != nil {
		return RunView{}, err
	}
	if err = c.Reconcile(context.WithoutCancel(ctx)); err != nil {
		return RunView{}, err
	}
	if err = c.FlushNotifications(context.WithoutCancel(ctx)); err != nil {
		return RunView{}, err
	}
	f, _, err = c.Fire(context.WithoutCancel(ctx), fireID)
	if err != nil {
		return RunView{}, err
	}
	return c.runView(context.WithoutCancel(ctx), f)
}
