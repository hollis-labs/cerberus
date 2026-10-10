package scheduling

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/libs/util/scheduler"
	_ "modernc.org/sqlite"
)

type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) Now() time.Time                         { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *testClock) advance(d time.Duration)                { c.mu.Lock(); defer c.mu.Unlock(); c.at = c.at.Add(d) }
func (*testClock) NewTicker(time.Duration) scheduler.Ticker { panic("tests tick explicitly") }

type executorFunc func(context.Context, Target, Admission) error

func (f executorFunc) Execute(ctx context.Context, target Target, admit Admission) error {
	return f(ctx, target, admit)
}

type authorityFunc func(context.Context, Request) (*Permit, error)

func (f authorityFunc) Authorize(ctx context.Context, r Request) (*Permit, error) { return f(ctx, r) }

func testPermit(r Request, now time.Time) *Permit {
	return &Permit{JobKey: r.Job.Key(), Revision: r.Revision, FireID: r.FireID, Target: r.Job.Target, PlanHash: r.PlanHash, ExpiresAt: now.Add(time.Minute)}
}
func allowAt(clock *testClock) Authorizer {
	return authorityFunc(func(_ context.Context, r Request) (*Permit, error) { return testPermit(r, clock.Now()), nil })
}
func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}
func testCore(t *testing.T, executor Executor, authority Authorizer, options Options) (*Core, *testClock) {
	t.Helper()
	clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	if options.Clock == nil {
		options.Clock = clock
	} else {
		clock = options.Clock.(*testClock)
	}
	c, err := New(context.Background(), openTestDB(t, filepath.Join(t.TempDir(), "jobs.db")), executor, authority, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	return c, clock
}
func dueJob(clock *testClock, kind TargetKind) Job {
	return Job{ID: "nightly", Name: "Nightly", OwnerApp: "test-app", Target: Target{Kind: kind, ID: "target"}, Timing: Timing{At: clock.Now()}, Timeout: time.Second, Enabled: true}
}
func mustCreate(t *testing.T, c *Core, j Job) {
	t.Helper()
	if err := c.Create(context.Background(), j); err != nil {
		t.Fatal(err)
	}
}
func tick(t *testing.T, c *Core) {
	t.Helper()
	if err := c.TickNow(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func fireFor(t *testing.T, c *Core, j Job, at time.Time) scheduler.Fire {
	t.Helper()
	f, ok, err := c.Fire(context.Background(), scheduler.DeriveFireID(j.Key(), at))
	if err != nil || !ok {
		t.Fatalf("fire unavailable: found=%v err=%v", ok, err)
	}
	return f
}
func receiptFor(t *testing.T, c *Core, f scheduler.Fire) Receipt {
	t.Helper()
	r, ok, err := c.Receipt(context.Background(), f.ID)
	if err != nil || !ok {
		t.Fatalf("receipt unavailable: found=%v err=%v", ok, err)
	}
	return r
}
func asDispatch(f scheduler.Fire) scheduler.Job {
	return scheduler.Job{ScheduleID: f.ScheduleID, FireID: f.ID, JobType: f.JobType, Payload: f.Payload, Attempt: f.Attempt, ScheduledAt: f.ScheduledAt}
}

func TestDurableInactiveCoreDispatchesFakeTargets(t *testing.T) {
	for _, kind := range []TargetKind{ResourceStart, ResourceDeploy, PipelineRun} {
		t.Run(string(kind), func(t *testing.T) {
			clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
			delivered := make(chan Target, 1)
			executor := executorFunc(func(ctx context.Context, target Target, admit Admission) error {
				if err := admit(ctx, "sha256:fake-current-plan", func() error { return nil }); err != nil {
					return err
				}
				delivered <- target
				return nil
			})
			path := filepath.Join(t.TempDir(), "jobs.db")
			db := openTestDB(t, path)
			c, err := New(context.Background(), db, executor, allowAt(clock), Options{Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			j := dueJob(clock, kind)
			mustCreate(t, c, j)
			if c.Status().Running {
				t.Fatal("construction started an engine")
			}
			select {
			case <-delivered:
				t.Fatal("opening/creating fired a job")
			default:
			}
			if closeErr := db.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			c, err = New(context.Background(), openTestDB(t, path), executor, allowAt(clock), Options{Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(c.Stop)
			restored, ok, err := c.Get(context.Background(), j.OwnerApp, j.ID)
			if err != nil || !ok || restored.Target != j.Target {
				t.Fatalf("reopened job: %+v %v %v", restored, ok, err)
			}
			tick(t, c)
			if got := <-delivered; got != j.Target {
				t.Fatalf("wrong target: %+v", got)
			}
			f := fireFor(t, c, j, j.Timing.At)
			if f.Status != scheduler.FireSucceeded || receiptFor(t, c, f).State != Completed {
				t.Fatalf("dispatch not completed: %+v", f)
			}
			tick(t, c)
			select {
			case <-delivered:
				t.Fatal("one-off delivered again")
			default:
			}
			if err := c.Enqueue(context.Background(), asDispatch(f)); !errors.Is(err, scheduler.ErrDuplicateJob) {
				t.Fatalf("receipt permitted duplicate: %v", err)
			}
			if receiptFor(t, c, f).State != Completed {
				t.Fatal("duplicate changed original outcome")
			}
		})
	}
}

func TestDisabledAndAbsentAuthorityRefuse(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-policy", true: "disabled"}[disabled], func(t *testing.T) {
			delivered := make(chan struct{}, 1)
			c, clock := testCore(t, executorFunc(func(context.Context, Target, Admission) error { delivered <- struct{}{}; return nil }), nil, Options{})
			j := dueJob(clock, ResourceStart)
			j.Enabled = !disabled
			mustCreate(t, c, j)
			tick(t, c)
			select {
			case <-delivered:
				t.Fatal("unattended effect ran")
			default:
			}
			f, found, err := c.Fire(context.Background(), scheduler.DeriveFireID(j.Key(), j.Timing.At))
			if err != nil {
				t.Fatal(err)
			}
			if disabled {
				if found {
					t.Fatal("disabled job materialized a fire")
				}
			} else if !found || f.Status != scheduler.FireExhausted || !strings.Contains(f.LastError, "authorization") {
				t.Fatalf("missing denial history: %+v", f)
			}
		})
	}
}

func TestFreshPermitBindingsRefuseBeforeSend(t *testing.T) {
	cases := map[string]func(*Permit){
		"expired":        func(p *Permit) { p.ExpiresAt = time.Time{} },
		"wrong-fire":     func(p *Permit) { p.FireID = "other" },
		"wrong-revision": func(p *Permit) { p.Revision = "other" },
		"wrong-job":      func(p *Permit) { p.JobKey = "other/nightly" },
		"changed-target": func(p *Permit) { p.Target.ID = "other" },
		"changed-plan":   func(p *Permit) { p.PlanHash = "old-plan" },
		"nil":            nil,
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
			delivered := make(chan struct{}, 1)
			authority := authorityFunc(func(_ context.Context, r Request) (*Permit, error) {
				if change == nil {
					return nil, nil
				}
				p := testPermit(r, clock.Now())
				change(p)
				return p, nil
			})
			c, _ := testCore(t, executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
				if err := admit(ctx, "current-plan", func() error { return nil }); err != nil {
					return err
				}
				delivered <- struct{}{}
				return nil
			}), authority, Options{Clock: clock})
			j := dueJob(clock, PipelineRun)
			mustCreate(t, c, j)
			tick(t, c)
			f := fireFor(t, c, j, j.Timing.At)
			if f.Status != scheduler.FireExhausted || receiptFor(t, c, f).State != Failed {
				t.Fatalf("refusal not recorded: %+v", f)
			}
			select {
			case <-delivered:
				t.Fatal("invalid permit sent an effect")
			default:
			}
		})
	}
}

func TestRevocationAndDeletionAreFreshAtEffectAdmission(t *testing.T) {
	for _, action := range []string{"revoke", "delete", "change-revision"} {
		t.Run(action, func(t *testing.T) {
			clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
			var c *Core
			ready, release := make(chan struct{}), make(chan struct{})
			var revoked bool
			authority := authorityFunc(func(_ context.Context, r Request) (*Permit, error) {
				if revoked {
					return nil, errors.New("revoked")
				}
				return testPermit(r, clock.Now()), nil
			})
			var sent bool
			c, _ = testCore(t, executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
				close(ready)
				<-release
				if err := admit(ctx, "current-plan", func() error { return nil }); err != nil {
					return err
				}
				sent = true
				return nil
			}), authority, Options{Clock: clock})
			j := dueJob(clock, ResourceDeploy)
			mustCreate(t, c, j)
			done := make(chan error, 1)
			go func() { done <- c.TickNow(context.Background()) }()
			<-ready
			switch action {
			case "revoke":
				revoked = true
			case "delete":
				if err := c.Delete(context.Background(), j.OwnerApp, j.ID); err != nil {
					t.Fatal(err)
				}
			case "change-revision":
				if err := c.Delete(context.Background(), j.OwnerApp, j.ID); err != nil {
					t.Fatal(err)
				}
				j.Target.ID = "changed"
				mustCreate(t, c, j)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if sent {
				t.Fatal("stale authority or job dispatched")
			}
			f := fireFor(t, c, j, j.Timing.At)
			if f.Status != scheduler.FireExhausted || receiptFor(t, c, f).State != Failed {
				t.Fatalf("denial missing: %+v", f)
			}
		})
	}
}

func TestUnknownPostDeliveryAndCrashReceiptsNeverReplay(t *testing.T) {
	for _, state := range []ReceiptState{Prepared, Sent, Unknown} {
		t.Run(string(state), func(t *testing.T) {
			clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
			delivered := make(chan struct{}, 1)
			c, _ := testCore(t, executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
				if err := admit(ctx, "current-plan", func() error { return nil }); err != nil {
					return err
				}
				delivered <- struct{}{}
				return errors.New("connection lost after delivery")
			}), allowAt(clock), Options{Clock: clock})
			j := dueJob(clock, ResourceDeploy)
			mustCreate(t, c, j)
			id := scheduler.DeriveFireID(j.Key(), j.Timing.At)
			if state != Unknown {
				revision, err := j.Revision()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.db.Exec(`INSERT INTO cerberus_schedule_receipts(fire_id,job_key,revision,state) VALUES(?,?,?,?)`, id, j.Key(), revision, state); err != nil {
					t.Fatal(err)
				}
			}
			tick(t, c)
			f := fireFor(t, c, j, j.Timing.At)
			if state == Unknown {
				<-delivered
				if f.Status != scheduler.FireExhausted || receiptFor(t, c, f).State != Unknown {
					t.Fatalf("post-delivery failure reported success: %+v", f)
				}
			} else if f.Status != scheduler.FireSkipped || receiptFor(t, c, f).State != state {
				t.Fatalf("crash receipt not preserved: %+v", f)
			}
			if err := c.Enqueue(context.Background(), asDispatch(f)); !errors.Is(err, scheduler.ErrDuplicateJob) {
				t.Fatalf("ambiguous dispatch replayed: %v", err)
			}
			select {
			case <-delivered:
				t.Fatal("ambiguous effect redelivered")
			default:
			}
		})
	}
}

func TestTimeoutIsCooperativeAndErrorValuesAreRedacted(t *testing.T) {
	clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	c, _ := testCore(t, executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
		if err := admit(ctx, "current-plan", func() error { return nil }); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}), allowAt(clock), Options{Clock: clock})
	j := dueJob(clock, PipelineRun)
	j.Timeout = 10 * time.Millisecond
	mustCreate(t, c, j)
	tick(t, c)
	f := fireFor(t, c, j, j.Timing.At)
	if f.Status != scheduler.FireExhausted || receiptFor(t, c, f).State != Unknown || !strings.Contains(f.LastError, "deadline") {
		t.Fatalf("deadline outcome: %+v", f)
	}
	c.executor = executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
		redact.ScopeFrom(ctx).Add("test-token", "unlabelled-private-value-0115")
		if err := admit(ctx, "current-plan", func() error { return nil }); err != nil {
			return err
		}
		return errors.New("provider echoed unlabelled-private-value-0115")
	})
	j.ID = "redaction"
	mustCreate(t, c, j)
	tick(t, c)
	f = fireFor(t, c, j, j.Timing.At)
	if strings.Contains(f.LastError, "unlabelled-private-value-0115") || strings.Contains(receiptFor(t, c, f).Error, "unlabelled-private-value-0115") {
		t.Fatal("resolved value reached durable error history")
	}
}

func TestLibraryRecurrenceMisfireAndConcurrency(t *testing.T) {
	clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	entered, release := make(chan Target, 2), make(chan struct{})
	c, _ := testCore(t, executorFunc(func(ctx context.Context, target Target, admit Admission) error {
		if err := admit(ctx, "current-plan", func() error { return nil }); err != nil {
			return err
		}
		entered <- target
		<-release
		return nil
	}), allowAt(clock), Options{Clock: clock, Concurrency: 1})
	j := dueJob(clock, ResourceStart)
	j.Timeout = 5 * time.Second
	mustCreate(t, c, j)
	other := j
	other.ID = "second"
	other.Target.ID = "second"
	mustCreate(t, c, other)
	done := make(chan error, 1)
	go func() { done <- c.TickNow(context.Background()) }()
	<-entered
	select {
	case <-entered:
		t.Fatal("engine exceeded configured concurrency")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-entered
	c.executor = executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
		return admit(ctx, "current-plan", func() error { return nil })
	})
	j.ID = "interval"
	j.Timing = Timing{Interval: time.Minute}
	j.Misfire = scheduler.MisfireSkip
	j.MisfireGrace = time.Second
	first := clock.Now().Add(time.Minute)
	mustCreate(t, c, j)
	clock.advance(10 * time.Minute)
	tick(t, c)
	f := fireFor(t, c, j, first)
	if f.Status != scheduler.FireSkipped || f.Reason != "misfire_skip" {
		t.Fatalf("stale interval dispatched: %+v", f)
	}
}

func TestJobValidationAndNamesOnlyEnvironment(t *testing.T) {
	c, clock := testCore(t, nil, nil, Options{})
	for _, mutate := range []func(*Job){
		func(j *Job) { j.Target.Kind = "command" }, func(j *Job) { j.Timeout = 0 }, func(j *Job) { j.Timeout = time.Hour },
		func(j *Job) { j.Timing.Cron = "* * * * *" }, func(j *Job) { j.Timing = Timing{Cron: "broken"} },
		func(j *Job) { j.Timing.Location = "Local" }, func(j *Job) { j.OwnerApp = "other/app" },
		func(j *Job) { j.EnvRefs = []EnvReference{{Env: "API_TOKEN", Ref: "literal=value"}} },
	} {
		j := dueJob(clock, ResourceStart)
		mutate(&j)
		if err := c.Create(context.Background(), j); err == nil {
			t.Fatalf("invalid job accepted: %+v", j)
		}
	}
	j := dueJob(clock, ResourceStart)
	j.EnvRefs = []EnvReference{{Env: "API_TOKEN", Ref: "api-token"}}
	mustCreate(t, c, j)
	tick(t, c)
	f := fireFor(t, c, j, j.Timing.At)
	if f.Status != scheduler.FireExhausted || !strings.Contains(f.LastError, "environment delivery") {
		t.Fatalf("environment references silently ignored: %+v", f)
	}
}

func TestExpiredClaimRecoveryKeepsUnknownReceiptAcrossReopen(t *testing.T) {
	clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "jobs.db")
	db := openTestDB(t, path)
	never := executorFunc(func(context.Context, Target, Admission) error { t.Error("crashed dispatch was replayed"); return nil })
	c, err := New(context.Background(), db, never, allowAt(clock), Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	j := dueJob(clock, ResourceDeploy)
	mustCreate(t, c, j)
	sch, err := j.schedule(clock.Now(), c.maxTimeout)
	if err != nil {
		t.Fatal(err)
	}
	f := scheduler.Fire{ID: scheduler.DeriveFireID(j.Key(), j.Timing.At), ScheduleID: j.Key(), ScheduledAt: j.Timing.At, Status: scheduler.FirePending, NextAttemptAt: j.Timing.At, Retry: sch.Retry, JobType: jobType, Payload: sch.Payload}
	created, createErr := c.store.CreateFire(context.Background(), scheduler.FireCreation{ScheduleID: j.Key(), ExpectedNext: sch.NextRun, NextRun: sch.NextRun.Add(time.Hour), Fire: f})
	if createErr != nil || !created {
		t.Fatalf("materialize: %v %v", created, createErr)
	}
	_, claimed, claimErr := c.store.ClaimFire(context.Background(), scheduler.FireClaim{FireID: f.ID, ExpectedStatus: scheduler.FirePending, ClaimedAt: clock.Now(), ClaimExpiresAt: clock.Now().Add(time.Second)})
	if claimErr != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, claimErr)
	}
	if disableErr := c.store.DisableSchedule(context.Background(), j.Key()); disableErr != nil {
		t.Fatal(disableErr)
	}
	revision, revisionErr := j.Revision()
	if revisionErr != nil {
		t.Fatal(revisionErr)
	}
	if _, writeErr := db.Exec(`INSERT INTO cerberus_schedule_receipts(fire_id,job_key,revision,state) VALUES(?,?,?,?)`, f.ID, j.Key(), revision, Sent); writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	clock.advance(2 * time.Second)
	c, err = New(context.Background(), openTestDB(t, path), never, allowAt(clock), Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	tick(t, c)
	recovered := fireFor(t, c, j, j.Timing.At)
	if recovered.Status != scheduler.FireSkipped || receiptFor(t, c, recovered).State != Sent {
		t.Fatalf("ambiguous crash recovery changed outcome: %+v", recovered)
	}
	history, historyErr := c.History(context.Background(), j.OwnerApp, j.ID, 10)
	if historyErr != nil || len(history) == 0 || history[0].ID != recovered.ID {
		t.Fatalf("durable history unavailable: %+v %v", history, historyErr)
	}
}

func TestOverlapSkipAndQueueUseSharedClaims(t *testing.T) {
	for _, overlap := range []scheduler.OverlapPolicy{scheduler.OverlapSkip, scheduler.OverlapQueue} {
		t.Run(string(overlap), func(t *testing.T) {
			clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
			entered, release := make(chan struct{}, 2), make(chan struct{})
			c, _ := testCore(t, executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
				if err := admit(ctx, "current-plan", func() error { return nil }); err != nil {
					return err
				}
				entered <- struct{}{}
				<-release
				return nil
			}), allowAt(clock), Options{Clock: clock, Concurrency: 2})
			j := dueJob(clock, ResourceStart)
			j.Timeout = 5 * time.Second
			j.Timing = Timing{Interval: time.Minute}
			j.Overlap = overlap
			mustCreate(t, c, j)
			clock.advance(time.Minute)
			first := clock.Now()
			done := make(chan error, 1)
			go func() { done <- c.TickNow(context.Background()) }()
			<-entered
			clock.advance(time.Minute)
			second := clock.Now()
			tick(t, c)
			select {
			case <-entered:
				t.Fatal("overlapping claim dispatched")
			default:
			}
			f := fireFor(t, c, j, second)
			if overlap == scheduler.OverlapSkip {
				if f.Status != scheduler.FireSkipped || f.Reason != "overlap_skip" {
					t.Fatalf("overlap not skipped: %+v", f)
				}
			} else if f.Status != scheduler.FirePending {
				t.Fatalf("overlap not queued: %+v", f)
			}
			close(release)
			if tickErr := <-done; tickErr != nil {
				t.Fatal(tickErr)
			}
			if fireFor(t, c, j, first).Status != scheduler.FireSucceeded {
				t.Fatal("original claim did not complete")
			}
			if overlap == scheduler.OverlapQueue {
				tick(t, c)
				<-entered
				if fireFor(t, c, j, second).Status != scheduler.FireSucceeded {
					t.Fatal("queued claim did not complete after prior claim")
				}
			}
		})
	}
}

func TestPortableCronLocation(t *testing.T) {
	clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	c, _ := testCore(t, executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
		return admit(ctx, "current-plan", func() error { return nil })
	}), allowAt(clock), Options{Clock: clock})
	j := dueJob(clock, PipelineRun)
	j.Timing = Timing{Cron: "0 9 * * *", Location: "America/New_York"}
	mustCreate(t, c, j)
	s, found, err := c.store.GetSchedule(context.Background(), j.Key())
	want := time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC)
	if err != nil || !found || !s.NextRun.Equal(want) {
		t.Fatalf("portable local cron: %+v %v", s, err)
	}
	clock.advance(time.Hour)
	tick(t, c)
	if fireFor(t, c, j, want).Status != scheduler.FireSucceeded {
		t.Fatal("named-zone cron did not dispatch at local occurrence")
	}
}
