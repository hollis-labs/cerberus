package scheduling

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/libs/util/scheduler"
	"github.com/hollis-labs/libs/util/scheduler/sqlstore"
)

// Request is recomputed at effect admission, including the current runtime plan.
type Request struct {
	Job      Job
	Revision string
	FireID   string
	PlanHash string
}

// DispatchInfo labels runtime audit records; it is never authorization.
type DispatchInfo struct{ JobKey, FireID, Revision string }
type dispatchKey struct{}

func DispatchFrom(ctx context.Context) (DispatchInfo, bool) {
	d, ok := ctx.Value(dispatchKey{}).(DispatchInfo)
	return d, ok
}

// Permit is ephemeral: it is never stored with a job or reused for another fire.
// A trusted authorizer must consult current revocation/policy state on every call.
type Permit struct {
	JobKey    string
	Revision  string
	FireID    string
	Target    Target
	PlanHash  string
	ExpiresAt time.Time
}

// Authorizer is a trusted host dependency, not a job-supplied policy or approval.
// This task supplies no production implementation. Nil always refuses.
type Authorizer interface {
	Authorize(context.Context, Request) (*Permit, error)
}

// Admission must be called once, by the serving runtime after its ordinary
// brakes/policy/audit gates, using the exact plan it will execute. Executors
// must honor ctx, perform no effects before admission, and never retry delivery.
type Admission func(context.Context, string, func() error) error

// Executor is the platform seam. The Cerberus adapter uses shared runtime APIs;
// tests inject fakes. No spawning, paths, signals or OS locks live in this core.
type Executor interface {
	Execute(context.Context, Target, Admission) error
}

type Options struct {
	Clock          scheduler.Clock
	Concurrency    int
	MaxFireTimeout time.Duration
}

// Core borrows an application-owned SQLite DB; its caller owns closing it.
// Opening/migrating the store never fires jobs. Start or TickNow is explicit.
type Core struct {
	store         *sqlstore.Store
	db            *sql.DB
	engine        *scheduler.Engine
	authority     Authorizer
	executor      Executor
	now           func() time.Time
	maxTimeout    time.Duration
	dispatchSlots chan struct{}
}

func New(ctx context.Context, db *sql.DB, executor Executor, authority Authorizer, options Options) (*Core, error) {
	if db == nil {
		return nil, errors.New("scheduler requires an application-owned database")
	}
	if options.Concurrency < 0 || options.MaxFireTimeout < 0 {
		return nil, errors.New("scheduler concurrency and deadline must not be negative")
	}
	store, err := sqlstore.New(db)
	if err != nil {
		return nil, err
	}
	if err := sqlstore.Migrate(ctx, db); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS cerberus_schedule_receipts (
		fire_id TEXT PRIMARY KEY, job_key TEXT NOT NULL, revision TEXT NOT NULL,
		state TEXT NOT NULL, plan_hash TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '')`); err != nil {
		return nil, err
	}
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS cerberus_schedule_generations(job_key TEXT PRIMARY KEY,generation INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS cerberus_schedule_creates(owner_app TEXT NOT NULL,idempotency_key TEXT NOT NULL,fingerprint TEXT NOT NULL,job_key TEXT NOT NULL,incarnation INTEGER NOT NULL,PRIMARY KEY(owner_app,idempotency_key))`,
		`CREATE TABLE IF NOT EXISTS cerberus_schedule_manual(job_key TEXT NOT NULL,request_id TEXT NOT NULL,fire_id TEXT NOT NULL,PRIMARY KEY(job_key,request_id))`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			return nil, err
		}
	}
	maxTimeout := options.MaxFireTimeout
	if maxTimeout == 0 {
		maxTimeout = scheduler.DefaultFireTimeout
	}
	if maxTimeout < 0 {
		return nil, errors.New("engine deadline must be positive")
	}
	c := &Core{store: store, db: db, executor: executor, authority: authority, now: time.Now, maxTimeout: maxTimeout}
	if options.Clock != nil {
		c.now = options.Clock.Now
	}
	concurrency := options.Concurrency
	if concurrency == 0 {
		concurrency = scheduler.DefaultConcurrency
	}
	c.dispatchSlots = make(chan struct{}, concurrency)
	c.engine = scheduler.New(store, c, scheduler.WithClock(options.Clock), scheduler.WithConcurrency(options.Concurrency), scheduler.WithFireTimeout(maxTimeout))
	return c, nil
}

func (c *Core) Create(ctx context.Context, job Job) error {
	if job.Generation != 0 || job.Incarnation != 0 {
		return redact.Guidance("scheduled job generation is server assigned")
	}
	_, err := c.createJob(ctx, job, uuid.NewString())
	return err
}

func (c *Core) Get(ctx context.Context, owner, id string) (Job, bool, error) {
	s, found, err := c.store.GetSchedule(ctx, owner+"/"+id)
	if err != nil || !found {
		return Job{}, found, err
	}
	j, err := decodeJob(s.Payload)
	return j, true, err
}

func (c *Core) List(ctx context.Context, owner string) ([]Job, error) {
	schedules, err := c.store.ListSchedules(ctx)
	if err != nil {
		return nil, err
	}
	jobs := []Job{}
	for _, s := range schedules {
		j, err := decodeJob(s.Payload)
		if err != nil {
			return nil, err
		}
		if j.OwnerApp == owner {
			jobs = append(jobs, j)
		}
	}
	return jobs, nil
}

// Delete preserves fire history and receipts. Pending fires subsequently refuse.
func (c *Core) Delete(ctx context.Context, owner, id string) error {
	return c.store.DeleteSchedule(ctx, owner+"/"+id)
}
func (c *Core) Start()                            { c.engine.Start() }
func (c *Core) Stop()                             { c.engine.Stop() }
func (c *Core) TickNow(ctx context.Context) error { return c.engine.TickNow(ctx) }
func (c *Core) Status() scheduler.Status          { return c.engine.Status() }

// Fire is library dispatch history, not proof a started resource later finished.
func (c *Core) Fire(ctx context.Context, id string) (scheduler.Fire, bool, error) {
	return c.store.GetFire(ctx, id)
}

// History reads library fire records, newest first. Receipts distinguish
// completed dispatch from ambiguous delivery and must be inspected with them.
func (c *Core) History(ctx context.Context, owner, id string, limit int) ([]scheduler.Fire, error) {
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("history limit must be between 1 and 1000")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT id FROM gosched_fires WHERE schedule_id=? ORDER BY scheduled_at DESC,id LIMIT ?`, owner+"/"+id, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var fireID string
		if scanErr := rows.Scan(&fireID); scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		ids = append(ids, fireID)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	history := []scheduler.Fire{}
	for _, fireID := range ids {
		fire, found, err := c.store.GetFire(ctx, fireID)
		if err != nil {
			return nil, err
		}
		if found {
			history = append(history, fire)
		}
	}
	return history, nil
}

// Prune uses the library's retention/high-water fences. Effect receipts are
// deliberately retained: history deletion must never license effect replay.
func (c *Core) Prune(ctx context.Context, olderThan time.Time) (int, error) {
	return c.store.Prune(ctx, olderThan)
}

type ReceiptState string

// ErrExecutionFailed is a confirmed application execution failure. Transport
// loss and cancellation after admission remain Unknown, never this sentinel.
var ErrExecutionFailed = errors.New("scheduled execution reported failure")

const (
	Prepared  ReceiptState = "prepared"
	Sent      ReceiptState = "sent"
	Completed ReceiptState = "completed"
	Failed    ReceiptState = "failed"
	Unknown   ReceiptState = "unknown"
)

// Receipt is the write-ahead effect record. A prepared/sent record left after
// a crash needs explicit reconciliation; reopening never resets or redelivers it.
type Receipt struct {
	FireID   string       `json:"fire_id"`
	JobKey   string       `json:"job_key"`
	Revision string       `json:"revision"`
	State    ReceiptState `json:"state"`
	PlanHash string       `json:"plan_hash"`
	Error    string       `json:"error,omitempty"`
}

func (c *Core) Receipt(ctx context.Context, id string) (Receipt, bool, error) {
	var r Receipt
	err := c.db.QueryRowContext(ctx, `SELECT fire_id,job_key,revision,state,plan_hash,error FROM cerberus_schedule_receipts WHERE fire_id=?`, id).Scan(&r.FireID, &r.JobKey, &r.Revision, &r.State, &r.PlanHash, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func decodeJob(data []byte) (Job, error) {
	var j Job
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	err := d.Decode(&j)
	if err == nil {
		var extra any
		if d.Decode(&extra) != io.EOF {
			err = errors.New("unexpected data after scheduled job")
		}
	}
	return j, err
}

func (c *Core) current(ctx context.Context, fire scheduler.Job) (Job, error) {
	s, found, err := c.store.GetSchedule(ctx, fire.ScheduleID)
	if err != nil {
		return Job{}, err
	}
	if !found || s.JobType != jobType || !bytes.Equal(s.Payload, fire.Payload) {
		return Job{}, redact.Guidance("scheduled job is missing or its revision changed; nothing was sent")
	}
	j, err := decodeJob(fire.Payload)
	if err != nil {
		return j, redact.Guidance("invalid scheduled job payload; nothing was sent")
	}
	if _, err := j.schedule(c.now(), c.maxTimeout); err != nil || j.Key() != fire.ScheduleID || !j.Enabled {
		return j, redact.Guidance("scheduled job is disabled or invalid; nothing was sent")
	}
	return j, nil
}

// Enqueue runs synchronously within the library's cooperative deadline. Both
// normal errors and timeouts exhaust this fire; effects are never retried.
func (c *Core) Enqueue(ctx context.Context, fire scheduler.Job) (retErr error) {
	ctx, scope := redact.EnsureScope(ctx)
	defer func() {
		if retErr != nil {
			retErr = scope.Error(retErr)
		}
	}()
	if fire.JobType != jobType || fire.FireID != scheduler.DeriveFireID(fire.ScheduleID, fire.ScheduledAt) || fire.Attempt != 1 {
		return redact.Guidance("unknown scheduled fire; nothing was sent")
	}
	// One bound is shared by library dispatch and selected manual fires.
	// Waiting remains inside the cooperative deadline; admission rechecks the
	// durable claim after the slot is obtained.
	select {
	case c.dispatchSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.dispatchSlots }()
	j, err := c.current(ctx, fire)
	if err != nil {
		return err
	}
	if len(j.EnvRefs) != 0 {
		return redact.Guidance("scheduled environment delivery is not implemented; nothing was sent")
	}
	if c.executor == nil || c.authority == nil {
		return redact.Guidance("scheduled effects require explicit per-fire policy authorization; nothing was sent")
	}
	if _, exists, receiptErr := c.Receipt(ctx, fire.FireID); receiptErr != nil {
		return receiptErr
	} else if exists {
		return fmt.Errorf("%w: effect receipt exists; reconcile the previous attempt explicitly", scheduler.ErrDuplicateJob)
	}
	if claimErr := c.checkClaim(ctx, fire); claimErr != nil {
		return claimErr
	}
	revision, err := j.Revision()
	if err != nil {
		return err
	}
	// Reserve before giving an executor the request. Even a crash during
	// preparation is ambiguous to a recovering worker, never a replay license.
	res, err := c.db.ExecContext(ctx, `INSERT INTO cerberus_schedule_receipts(fire_id,job_key,revision,state) VALUES(?,?,?,?) ON CONFLICT(fire_id) DO NOTHING`, fire.FireID, j.Key(), revision, Prepared)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: effect receipt exists; reconcile the previous attempt explicitly", scheduler.ErrDuplicateJob)
	}
	ctx, cancel := context.WithTimeout(ctx, j.Timeout)
	defer cancel()
	ctx = context.WithValue(ctx, dispatchKey{}, DispatchInfo{JobKey: j.Key(), FireID: fire.FireID, Revision: revision})
	admit := func(effectCtx context.Context, planHash string, recheck func() error) error {
		if contextErr := effectCtx.Err(); contextErr != nil {
			return contextErr
		}
		if _, currentErr := c.current(effectCtx, fire); currentErr != nil {
			return currentErr
		}
		if claimErr := c.checkClaim(effectCtx, fire); claimErr != nil {
			return claimErr
		}
		if planHash == "" {
			return redact.Guidance("scheduled target has no current plan; nothing was sent")
		}
		req := Request{Job: j, Revision: revision, FireID: fire.FireID, PlanHash: planHash}
		permit, authorizeErr := c.authority.Authorize(effectCtx, req)
		if authorizeErr != nil {
			return redact.Guidance("scheduled policy authorization refused; nothing was sent")
		}
		if permit == nil || permit.JobKey != j.Key() || permit.Revision != revision || permit.FireID != fire.FireID || permit.Target != j.Target || permit.PlanHash != planHash || !permit.ExpiresAt.After(c.now()) {
			return redact.Guidance("scheduled permit is absent, expired or does not match this fire and target plan; nothing was sent")
		}
		if contextErr := effectCtx.Err(); contextErr != nil {
			return contextErr
		}
		if recheck == nil {
			return redact.Guidance("scheduled effect has no runtime policy recheck; nothing was sent")
		}
		if policyErr := recheck(); policyErr != nil {
			return policyErr
		}
		if _, currentErr := c.current(effectCtx, fire); currentErr != nil {
			return currentErr
		}
		if claimErr := c.checkClaim(effectCtx, fire); claimErr != nil {
			return claimErr
		}
		if !permit.ExpiresAt.After(c.now()) {
			return redact.Guidance("scheduled permit expired during admission; nothing was sent")
		}
		sentResult, writeErr := c.db.ExecContext(effectCtx, `UPDATE cerberus_schedule_receipts SET state=?,plan_hash=? WHERE fire_id=? AND state=? AND EXISTS(SELECT 1 FROM gosched_schedules WHERE id=? AND payload=?) AND EXISTS(SELECT 1 FROM gosched_fires WHERE id=? AND status=? AND attempt=? AND fired_at=? AND claim_expires_at>?)`, Sent, planHash, fire.FireID, Prepared, fire.ScheduleID, fire.Payload, fire.FireID, scheduler.FireClaimed, fire.Attempt, stamp(fire.FiredAt), stamp(c.now()))
		if writeErr != nil {
			return writeErr
		}
		affected, affectedErr := sentResult.RowsAffected()
		if affectedErr != nil {
			return affectedErr
		}
		if affected != 1 {
			return redact.Guidance("scheduled effect was already admitted; refusing another delivery")
		}
		return nil
	}
	err = c.executor.Execute(ctx, j.Target, admit)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	r, found, readErr := c.Receipt(context.WithoutCancel(ctx), fire.FireID)
	if readErr != nil || !found {
		return redact.Guidance("scheduled effect receipt could not be read; outcome is unknown")
	}
	state := Failed
	if r.State == Sent {
		state = Completed
		if err != nil {
			state = Unknown
			if errors.Is(err, ErrExecutionFailed) {
				state = Failed
			}
		}
	} else if err == nil {
		err = redact.Guidance("executor returned without admitting the scheduled effect")
	}
	text := ""
	if err != nil {
		text = redact.Render(scope, err)
	}
	if _, writeErr := c.db.ExecContext(context.WithoutCancel(ctx), `UPDATE cerberus_schedule_receipts SET state=?,error=? WHERE fire_id=?`, state, text, fire.FireID); writeErr != nil {
		return redact.Guidance("scheduled effect outcome could not be recorded; reconcile the receipt before recovery")
	}
	return err
}

func (c *Core) checkClaim(ctx context.Context, dispatch scheduler.Job) error {
	fire, found, err := c.store.GetFire(ctx, dispatch.FireID)
	if err != nil {
		return err
	}
	if !found || fire.Status != scheduler.FireClaimed || fire.Attempt != dispatch.Attempt || !fire.FiredAt.Equal(dispatch.FiredAt) || !fire.ClaimExpiresAt.After(c.now()) {
		return redact.Guidance("scheduled fire has no current dispatch claim; nothing was sent")
	}
	return nil
}
