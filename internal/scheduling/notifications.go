package scheduling

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hollis-labs/libs/util/scheduler"
)

// Notification is fixed safe metadata, never an error, payload or credential.
type Notification struct {
	EventID, JobKey, EpisodeID, Kind, FireID string
	Incarnation                              uint64
}

// NotificationSink is a trusted host binding. Idempotent means the receiver
// durably deduplicates EventID, including ambiguous transport failures/restarts.
// No production binding is supplied. Missing sinks remain unavailable.
type NotificationSink interface {
	Idempotent() bool
	Send(context.Context, Notification) error
}

// completeReceipt commits the safe outcome fact alongside the receipt. It
// preserves restart reconciliation even if public fire history is pruned.
func (c *Core) completeReceipt(ctx context.Context, j Job, f scheduler.Job, state ReceiptState, text string) error {
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return err
	}
	defer done()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gosched_fires WHERE id=? AND status=? AND attempt=? AND fired_at=? AND claim_expires_at>?`, f.FireID, scheduler.FireClaimed, f.Attempt, stamp(f.FiredAt), stamp(c.now())).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return errors.New("scheduled outcome has lost its claim; reconcile the receipt")
	}
	result, err := tx.ExecContext(ctx, `UPDATE cerberus_schedule_receipts SET state=?,error=? WHERE fire_id=? AND state IN ('prepared','sent')`, state, text, f.FireID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("receipt already completed")
	}

	if _, err = tx.ExecContext(ctx, `INSERT INTO cerberus_schedule_outcomes(fire_id,job_key,incarnation,scheduled_at,claimed_at,state) VALUES(?,?,?,?,?,?) ON CONFLICT(fire_id) DO NOTHING`, f.FireID, j.Key(), j.Incarnation, stamp(f.ScheduledAt), stamp(f.FiredAt), state); err != nil {
		return err
	}
	return tx.Commit()
}

// Reconcile ingests retained safe outcomes in chronological order. It performs
// no external delivery, so reads/prune/reopen never send live messages.
func (c *Core) Reconcile(ctx context.Context) error {
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return err
	}
	defer done()
	rows, err := tx.QueryContext(ctx, `SELECT fire_id,job_key,incarnation,scheduled_at,state FROM cerberus_schedule_outcomes WHERE observed=0 ORDER BY scheduled_at,fire_id LIMIT 128`)
	if err != nil {
		return err
	}
	type fact struct {
		id, key, at, state string
		inc                uint64
	}
	var facts []fact
	for rows.Next() {
		var f fact
		if err = rows.Scan(&f.id, &f.key, &f.inc, &f.at, &f.state); err != nil {
			_ = rows.Close()
			return err
		}
		facts = append(facts, f)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, f := range facts {
		kind := ""
		switch ReceiptState(f.state) {
		case Failed:
			kind = "failure"
		case Completed:
			kind = "success"
		default: // prepared/sent/unknown never become confirmed notification outcomes
		}
		if err = c.ingestTx(ctx, tx, Notification{EventID: "outcome/" + f.id, JobKey: f.key, Incarnation: f.inc, FireID: f.id, Kind: kind}, f.at+"/"+f.id+"/1"); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE cerberus_schedule_outcomes SET observed=1 WHERE fire_id=?`, f.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (c *Core) observe(ctx context.Context, event scheduler.ObserverEvent) error {
	if event.Kind == scheduler.ObserverMisfire {
		j, err := decodeJob(event.Fire.Payload)
		if err != nil {
			return err
		}
		if j.Key() != event.Fire.ScheduleID {
			return errors.New("misfire identity does not match job")
		}
		id := "misfire/" + event.Fire.ID
		if err = c.ingest(ctx, Notification{EventID: id, JobKey: j.Key(), Incarnation: j.Incarnation, FireID: event.Fire.ID, Kind: "misfire"}, stamp(event.Fire.ScheduledAt)+"/"+event.Fire.ID+"/0"); err != nil {
			return err
		}
	}
	if err := c.Reconcile(ctx); err != nil {
		return err
	}
	return c.FlushNotifications(ctx)
}

func (c *Core) ingest(ctx context.Context, event Notification, cursor string) error {
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err = c.ingestTx(ctx, tx, event, cursor); err != nil {
		return err
	}
	return tx.Commit()
}
func (c *Core) ingestTx(ctx context.Context, tx *sql.Tx, event Notification, cursor string) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO cerberus_schedule_observations(event_id) VALUES(?) ON CONFLICT(event_id) DO NOTHING`, event.EventID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO cerberus_schedule_episodes(job_key,incarnation) VALUES(?,?) ON CONFLICT DO NOTHING`, event.JobKey, event.Incarnation); err != nil {
		return err
	}
	var failing bool
	var episode, previous string
	var misfires int
	if err = tx.QueryRowContext(ctx, `SELECT episode_id,failing,misfires,cursor FROM cerberus_schedule_episodes WHERE job_key=? AND incarnation=?`, event.JobKey, event.Incarnation).Scan(&episode, &failing, &misfires, &previous); err != nil {
		return err
	}
	if cursor <= previous || event.Kind == "" {
		return nil
	}
	kind := ""
	switch event.Kind {
	case "misfire":
		misfires = min(misfires+1, 2)
		if misfires >= 2 && !failing {
			kind = "failure"
		}
	case "failure":
		if !failing {
			kind = "failure"
		}
	case "success":
		misfires = 0
		if failing {
			kind = "recovery"
		}
	}
	if kind == "failure" {
		failing = true
		episode = event.EventID
	}
	if kind == "recovery" {
		failing = false
	}
	if kind != "" {
		state := "pending"
		if c.notifications == nil {
			state = "unavailable"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO cerberus_schedule_outbox(event_id,job_key,incarnation,episode_id,kind,fire_id,state) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, fmt.Sprintf("%s/%d/%s/%s", event.JobKey, event.Incarnation, episode, kind), event.JobKey, event.Incarnation, episode, kind, event.FireID, state)
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE cerberus_schedule_episodes SET episode_id=?,failing=?,misfires=?,cursor=? WHERE job_key=? AND incarnation=?`, episode, failing, misfires, cursor, event.JobKey, event.Incarnation); err != nil {
		return err
	}
	return nil
}

// DeliverNotification is an explicit trusted host operation, not a user surface
// and not a scheduler loop. Non-idempotent reserved/uncertain events never retry.
func (c *Core) DeliverNotification(ctx context.Context, id string) (retErr error) {
	defer func() {
		if retErr != nil && ErrorCode(retErr) == "internal" {
			retErr = Refusal("unavailable", "notification storage is unavailable")
		}
	}()
	if c.notifications == nil {
		return Refusal("unavailable", "trusted notification binding is unavailable")
	}
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return err
	}
	defer done()
	var event Notification
	var state string
	err = tx.QueryRowContext(ctx, `SELECT event_id,job_key,incarnation,episode_id,kind,fire_id,state FROM cerberus_schedule_outbox WHERE event_id=?`, id).Scan(&event.EventID, &event.JobKey, &event.Incarnation, &event.EpisodeID, &event.Kind, &event.FireID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return Refusal("not_found", "notification event was not found")
	}
	if err != nil {
		return err
	}
	if state == "delivered" {
		return nil
	}
	if (state == "reserved" || state == "uncertain") && !c.notifications.Idempotent() {
		return Refusal("uncertain", "previous notification send may have succeeded; reconcile explicitly")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE cerberus_schedule_outbox SET state='reserved' WHERE event_id=?`, id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	done()
	sendErr := c.notifications.Send(ctx, event)
	next := "delivered"
	if sendErr != nil {
		next = "uncertain"
	}
	if _, err = c.db.ExecContext(context.WithoutCancel(ctx), `UPDATE cerberus_schedule_outbox SET state=? WHERE event_id=? AND state='reserved'`, next, id); err != nil {
		return Refusal("uncertain", "notification delivery outcome could not be recorded")
	}
	if sendErr != nil {
		return Refusal("uncertain", "notification delivery outcome is uncertain")
	}
	return nil
}

// FlushNotifications is bounded and is invoked by explicit engine/manual-run
// callbacks, never on construction, reads or pruning. A host may call it for
// recovery; only idempotent bindings permit retry of ambiguous reservations.
func (c *Core) FlushNotifications(ctx context.Context) error {
	if c.notifications == nil {
		return nil
	}
	query := `SELECT event_id FROM cerberus_schedule_outbox WHERE state IN ('pending','unavailable') ORDER BY rowid LIMIT 128`
	if c.notifications.Idempotent() {
		query = `SELECT event_id FROM cerberus_schedule_outbox WHERE state IN ('pending','unavailable','reserved','uncertain') ORDER BY rowid LIMIT 128`
	}
	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = c.DeliverNotification(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
