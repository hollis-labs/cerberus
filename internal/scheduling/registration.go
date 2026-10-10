package scheduling

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/hollis-labs/libs/util/scheduler"
)

const MaxRegistrationJobs = 32
const MaxAppJobs = 128

// Registration owns a desired definition, not the current pause/cursor state.
// Absent permits a first create or repetition of the same registered desired
// definition. A changed definition always needs the current explicit revision.
type Registration struct {
	Job      Job    `json:"job"`
	Absent   bool   `json:"absent,omitempty"`
	Revision string `json:"revision,omitempty"`
}
type registrationBinding struct {
	ID          string
	Incarnation uint64
}

func (c *Core) register(ctx context.Context, r Call) ([]JobView, error) {
	raw, err := json.Marshal(r.Registration)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	for _, entry := range r.Registration {
		if _, err = entry.Job.schedule(c.now(), c.maxTimeout); err != nil {
			return nil, Refusal("invalid", err.Error())
		}
	}
	tx, done, err := c.writeAccessTx(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	var previous string
	var bindingsRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,bindings FROM cerberus_schedule_registrations WHERE owner_app=? AND request_key=?`, r.OwnerApp, r.IdempotencyKey).Scan(&previous, &bindingsRaw)
	if err == nil {
		if previous != fingerprint {
			return nil, Refusal("conflict", "registration key was used for different content")
		}
		var bindings []registrationBinding
		if err = json.Unmarshal(bindingsRaw, &bindings); err != nil {
			return nil, err
		}
		out := []JobView{}
		for _, b := range bindings {
			v, e := registrationView(ctx, tx, r.OwnerApp, b.ID)
			if e != nil && ErrorCode(e) != "not_found" {
				return nil, e
			}
			if e != nil || v.Job.Incarnation != b.Incarnation {
				return nil, Refusal("conflict", "registered incarnation was deleted; request will not be replayed")
			}
			out = append(out, v)
		}
		if err = recheckAccess(ctx); err != nil {
			return nil, err
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	out := []JobView{}
	bindings := []registrationBinding{}
	for _, entry := range r.Registration {
		desired, e := entry.Job.Revision()
		if e != nil {
			return nil, e
		}
		current, e := registrationView(ctx, tx, r.OwnerApp, entry.Job.ID)
		found := e == nil
		if e != nil && ErrorCode(e) != "not_found" {
			return nil, e
		}
		if !found {
			if !entry.Absent {
				return nil, Refusal("conflict", "registration revision names an absent job")
			}
			if e = c.checkQuota(ctx, tx, r.OwnerApp, 1); e != nil {
				return nil, e
			}
			j := entry.Job
			j.Generation, e = nextGeneration(ctx, tx, j.Key())
			if e != nil {
				return nil, e
			}
			j.Incarnation = j.Generation
			sch, scheduleErr := j.schedule(c.now(), c.maxTimeout)
			if scheduleErr != nil {
				return nil, scheduleErr
			}
			if e = insertSchedule(ctx, tx, sch); e != nil {
				return nil, e
			}
			current, e = viewOf(j, sch)
			if e != nil {
				return nil, e
			}
		} else {
			var lastDesired string
			var incarnation uint64
			e = tx.QueryRowContext(ctx, `SELECT fingerprint,incarnation FROM cerberus_schedule_desired WHERE job_key=?`, entry.Job.Key()).Scan(&lastDesired, &incarnation)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			sameDesired := e == nil && incarnation == current.Job.Incarnation && lastDesired == desired
			if !sameDesired {
				if entry.Absent || current.Revision != entry.Revision {
					return nil, Refusal("conflict", "changed registration requires the current explicit revision")
				}
				j := entry.Job
				j.Incarnation = current.Job.Incarnation
				// Registration does not resume a paused job as a side effect of startup.
				j.Enabled = current.Job.Enabled
				j.Generation, e = nextGeneration(ctx, tx, j.Key())
				if e != nil {
					return nil, e
				}
				sch, scheduleErr := j.schedule(c.now(), c.maxTimeout)
				if scheduleErr != nil {
					return nil, scheduleErr
				}
				if current.State == "completed" {
					sch.Enabled = false
					sch.NextRun = current.NextRun
				}
				opts, optionsErr := scheduleOptions(sch)
				if optionsErr != nil {
					return nil, optionsErr
				}
				if _, e = tx.ExecContext(ctx, `UPDATE gosched_schedules SET cron_expr=?,next_run=?,enabled=?,payload=? WHERE id=?`, sch.CronExpr, stamp(sch.NextRun), sch.Enabled, sch.Payload, sch.ID); e != nil {
					return nil, e
				}
				if _, e = tx.ExecContext(ctx, `INSERT INTO gosched_schedule_options(schedule_id,options_json) VALUES(?,?) ON CONFLICT(schedule_id) DO UPDATE SET options_json=excluded.options_json`, sch.ID, string(opts)); e != nil {
					return nil, e
				}
				current, e = viewOf(j, sch)
				if e != nil {
					return nil, e
				}
			}
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO cerberus_schedule_desired(job_key,incarnation,fingerprint) VALUES(?,?,?) ON CONFLICT(job_key) DO UPDATE SET incarnation=excluded.incarnation,fingerprint=excluded.fingerprint`, entry.Job.Key(), current.Job.Incarnation, desired); e != nil {
			return nil, e
		}
		out = append(out, current)
		bindings = append(bindings, registrationBinding{entry.Job.ID, current.Job.Incarnation})
	}
	bindingsRaw, err = json.Marshal(bindings)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO cerberus_schedule_registrations(owner_app,request_key,fingerprint,bindings) VALUES(?,?,?,?)`, r.OwnerApp, r.IdempotencyKey, fingerprint, bindingsRaw); err != nil {
		return nil, err
	}
	if err = commitAccess(ctx, tx); err != nil {
		return nil, err
	}
	return out, nil
}

func registrationView(ctx context.Context, tx *sql.Tx, app, id string) (JobView, error) {
	var payload []byte
	var enabled bool
	var next string
	err := tx.QueryRowContext(ctx, `SELECT payload,enabled,next_run FROM gosched_schedules WHERE id=?`, app+"/"+id).Scan(&payload, &enabled, &next)
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
	nextRun, err := time.Parse(sqlTime, next)
	if err != nil {
		return JobView{}, err
	}
	sch := scheduler.Schedule{Enabled: enabled, NextRun: nextRun}
	return viewOf(j, sch)
}
