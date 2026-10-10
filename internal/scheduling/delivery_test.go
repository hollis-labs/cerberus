package scheduling

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/libs/util/scheduler"
)

type deliveryFixture struct {
	run func(context.Context, *Delivery) error
}

func (e deliveryFixture) Execute(context.Context, Target, Admission) error {
	return errors.New("delivery required")
}
func (e deliveryFixture) ExecuteDelivery(ctx context.Context, _ Target, admit Admission, d *Delivery) error {
	if err := admit(ctx, "test-plan", func() error { return nil }); err != nil {
		return err
	}
	return e.run(ctx, d)
}

type resolverFixture struct {
	value string
	calls int
}

func (r *resolverFixture) Resolve(context.Context, Request, EnvReference) (string, error) {
	r.calls++
	return r.value, nil
}

func TestDeliverySecretsAcrossEverySplitAndPrune(t *testing.T) {
	const sentinel = "resolved/Split\"Value 314159"
	resolver := &resolverFixture{value: sentinel}
	clock := &testClock{at: time.Now().UTC()}
	executor := deliveryFixture{run: func(_ context.Context, d *Delivery) error {
		env, err := d.Environ([]string{"PUBLIC=ok"})
		if err != nil {
			return err
		}
		if !strings.Contains(strings.Join(env, "\n"), "RUN_VALUE="+sentinel) {
			return errors.New("secret not delivered")
		}
		for _, form := range redact.Forms(sentinel) {
			for i := 1; i < len(form); i++ {
				if _, err = d.Stdout().Write([]byte(form[:i])); err != nil {
					return err
				}
				if _, err = d.Stdout().Write([]byte(form[i:] + "\n")); err != nil {
					return err
				}
			}
		}
		_, err = d.Stderr().Write([]byte(strings.Repeat("noise-", 200) + sentinel))
		return err
	}}
	c, _ := testCore(t, executor, allowAt(clock), Options{Clock: clock, Delivery: DeliveryOptions{Secrets: resolver, StreamBytes: 128, TotalBytes: 256}})
	j := dueJob(clock, PipelineRun)
	j.CaptureLogs = true
	j.EnvRefs = []EnvReference{{Env: "RUN_VALUE", Ref: "value-alias"}}
	mustCreate(t, c, j)
	tick(t, c)
	f := fireFor(t, c, j, j.Timing.At)
	if receiptFor(t, c, f).State != Completed {
		t.Fatal("delivery did not complete")
	}
	s := NewService(c, fixtureAccess)
	logs := callOK(t, s, Call{Operation: "logs", OwnerApp: j.OwnerApp, ID: j.ID, FireID: f.ID}).Logs
	if logs == nil || !logs.Available || !logs.Truncated || strings.Contains(logs.Stdout+logs.Stderr, sentinel) || !strings.Contains(logs.Stdout, "[REDACTED]") {
		t.Fatalf("unsafe/missing capture: %+v", logs)
	}
	var leaked int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM cerberus_schedule_logs WHERE instr(body,?)>0`, sentinel).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("stored secret %d %v", leaked, err)
	}
	callCode(t, s, Call{Operation: "logs", OwnerApp: "other", ID: j.ID, FireID: f.ID}, "not_found")
	if _, err := c.db.Exec(`CREATE TRIGGER refuse_test_prune BEFORE DELETE ON gosched_fires BEGIN SELECT RAISE(ABORT,'fixture prune refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Prune(context.Background(), clock.Now().Add(time.Hour)); err == nil {
		t.Fatal("prune unexpectedly succeeded")
	}
	retained := callOK(t, s, Call{Operation: "logs", OwnerApp: j.OwnerApp, ID: j.ID, FireID: f.ID}).Logs
	if retained == nil || !retained.Available || retained.Stdout != logs.Stdout || retained.Stderr != logs.Stderr {
		t.Fatal("failed public prune lost retained capture")
	}
	if _, err := c.db.Exec(`DROP TRIGGER refuse_test_prune`); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Prune(context.Background(), clock.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("prune %d %v", n, err)
	}
	callCode(t, s, Call{Operation: "logs", OwnerApp: j.OwnerApp, ID: j.ID, FireID: f.ID}, "not_found")
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM cerberus_schedule_logs`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("prune left logs", leaked, err)
	}
	if created, err := c.store.CreateFire(context.Background(), scheduler.FireCreation{ScheduleID: j.Key(), ExpectedNext: j.Timing.At, Fire: f}); err == nil && created {
		t.Fatal("pruned occurrence resurrected")
	}
}
func TestDeliveryRefusesUnprotectedSecretsBeforeEffect(t *testing.T) {
	for _, value := range []string{"short", "", "keychain://svc/key"} {
		t.Run(value, func(t *testing.T) {
			resolver := &resolverFixture{value: value}
			clock := &testClock{at: time.Now().UTC()}
			effects := 0
			c, _ := testCore(t, deliveryFixture{run: func(context.Context, *Delivery) error { effects++; return nil }}, allowAt(clock), Options{Clock: clock, Delivery: DeliveryOptions{Secrets: resolver}})
			j := dueJob(clock, PipelineRun)
			j.EnvRefs = []EnvReference{{Env: "RUN_VALUE", Ref: "alias"}}
			mustCreate(t, c, j)
			tick(t, c)
			f := fireFor(t, c, j, j.Timing.At)
			if effects != 0 || f.Status != scheduler.FireExhausted || strings.Contains(f.LastError, value) && value != "" {
				t.Fatal("unsafe secret accepted or leaked", f.LastError)
			}
		})
	}
}

type notificationFixture struct {
	mu       sync.Mutex
	idem     bool
	calls    int
	fail     bool
	received map[string]bool
}

func (n *notificationFixture) Idempotent() bool { return n.idem }
func (n *notificationFixture) Send(_ context.Context, e Notification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	if n.received == nil {
		n.received = map[string]bool{}
	}
	n.received[e.EventID] = true
	if n.fail {
		return errors.New("raw secret must not escape")
	}
	return nil
}
func TestEpisodesConcurrentReconcileAndUncertainDelivery(t *testing.T) {
	for _, idem := range []bool{false, true} {
		t.Run(fmt.Sprint(idem), func(t *testing.T) {
			sink := &notificationFixture{idem: idem, fail: true}
			c, clock := testCore(t, nil, nil, Options{Notifications: sink})
			ctx := context.Background()
			// Durable safe outcomes are the application's completion facts, surviving
			// history pruning and restart; no effect is executed by reconciliation.
			for i, state := range []ReceiptState{Failed, Failed, Unknown, Completed, Failed} {
				_, err := c.db.Exec(`INSERT INTO cerberus_schedule_outcomes(fire_id,job_key,incarnation,scheduled_at,claimed_at,state) VALUES(?,?,?,?,?,?)`, fmt.Sprint(i), "app/job", 1, stamp(clock.Now().Add(time.Duration(i)*time.Second)), stamp(clock.Now()), state)
				if err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); errs <- c.Reconcile(ctx) }()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			rows, err := c.db.Query(`SELECT event_id,kind FROM cerberus_schedule_outbox ORDER BY rowid`)
			if err != nil {
				t.Fatal(err)
			}
			var ids, kinds []string
			for rows.Next() {
				var id, kind string
				if err = rows.Scan(&id, &kind); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
				kinds = append(kinds, kind)
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			_ = rows.Close()
			if strings.Join(kinds, ",") != "failure,recovery,failure" {
				t.Fatal("episode dedup failed", kinds)
			}
			if err = c.DeliverNotification(ctx, ids[0]); ErrorCode(err) != "uncertain" || strings.Contains(err.Error(), "raw secret") {
				t.Fatal(err)
			}
			sink.fail = false
			err = c.DeliverNotification(ctx, ids[0])
			if idem {
				if err != nil || sink.calls != 2 || len(sink.received) != 1 {
					t.Fatal("idempotent recovery failed", err)
				}
			} else if ErrorCode(err) != "uncertain" || sink.calls != 1 {
				t.Fatal("ambiguous notification replayed", err)
			}
		})
	}
}
func TestMisfireEpisodeAndMissingSink(t *testing.T) {
	c, clock := testCore(t, nil, nil, Options{})
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := c.ingest(ctx, Notification{EventID: fmt.Sprint(i), JobKey: "app/job", Incarnation: 1, Kind: "misfire"}, stamp(clock.Now().Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	var id, state string
	if err := c.db.QueryRow(`SELECT event_id,state FROM cerberus_schedule_outbox`).Scan(&id, &state); err != nil || state != "unavailable" {
		t.Fatal(state, err)
	}
	var count int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM cerberus_schedule_outbox`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if ErrorCode(c.DeliverNotification(ctx, id)) != "unavailable" {
		t.Fatal("missing sink accepted")
	}
}

func TestEpisodeRestartRetainsDedupAndRecovery(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/jobs.db"
	clock := &testClock{at: time.Now().UTC()}
	first, err := New(ctx, openTestDB(t, path), nil, nil, Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	event := Notification{EventID: "failure-1", JobKey: "app/job", Incarnation: 1, Kind: "failure", FireID: "fire-one"}
	if err = first.ingest(ctx, event, stamp(clock.Now())); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(ctx, openTestDB(t, path), nil, nil, Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.ingest(ctx, event, stamp(clock.Now())); err != nil {
		t.Fatal(err)
	}
	if err = reopened.ingest(ctx, Notification{EventID: "success-1", JobKey: "app/job", Incarnation: 1, Kind: "success", FireID: "fire-two"}, stamp(clock.Now().Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = reopened.db.QueryRow(`SELECT COUNT(*) FROM cerberus_schedule_outbox`).Scan(&count); err != nil || count != 2 {
		t.Fatal("restart episode duplicated/lost recovery", count, err)
	}
}
func TestLogIncarnationAndGlobalCapacity(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{at: time.Now().UTC()}
	c, _ := testCore(t, deliveryFixture{run: func(_ context.Context, d *Delivery) error {
		_, err := d.Stdout().Write([]byte(strings.Repeat("public-output-", 40)))
		return err
	}}, allowAt(clock), Options{Clock: clock, Delivery: DeliveryOptions{StreamBytes: 128, TotalBytes: 128}})
	j := dueJob(clock, PipelineRun)
	j.CaptureLogs = true
	mustCreate(t, c, j)
	tick(t, c)
	fire := fireFor(t, c, j, j.Timing.At)
	logs, err := c.Logs(ctx, j.OwnerApp, j.ID, fire.ID)
	if err != nil || len(logs.Stdout) > 128 || !logs.Truncated {
		t.Fatal(logs, err)
	}
	old, _, err := c.Get(ctx, j.OwnerApp, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(c, fixtureAccess)
	revision, _ := old.Revision()
	callOK(t, s, Call{Operation: "delete", OwnerApp: j.OwnerApp, ID: j.ID, Revision: revision})
	j.Timing.At = clock.Now().Add(time.Second)
	callOK(t, s, Call{Operation: "create", Job: &j, IdempotencyKey: "new"})
	logs, err = c.Logs(ctx, j.OwnerApp, j.ID, fire.ID)
	if err != nil || logs.Available || logs.Stdout != "" {
		t.Fatal("old incarnation logs exposed", logs, err)
	}
	// Simulate the public-prune/cleanup crash window: orphan bytes remain until
	// reopen/maintenance, never become readable or free capacity prematurely.
	if _, err = c.db.Exec(`DELETE FROM gosched_fires WHERE id=?`, fire.ID); err != nil {
		t.Fatal(err)
	}
	if err = c.cleanupLogs(ctx); err != nil {
		t.Fatal(err)
	}
	var bytes int
	if err = c.db.QueryRow(`SELECT COALESCE(SUM(length(CAST(body AS BLOB))),0) FROM cerberus_schedule_logs`).Scan(&bytes); err != nil || bytes != 0 {
		t.Fatal("orphan cleanup failed", bytes, err)
	}
}

type secretReaderFixture struct{ service, key, value string }

func (r *secretReaderFixture) Get(_ context.Context, service, key string) (string, error) {
	r.service, r.key = service, key
	return r.value, nil
}
func TestSecretBindingsAreImmutableWhileValuesRotate(t *testing.T) {
	target := Target{Kind: PipelineRun, ID: "pipeline"}
	mappings := map[string]SecretBinding{"alias": {Target: target, Env: "RUN_VALUE", Service: "permitted", Key: "key"}}
	reader := &secretReaderFixture{value: "first-fake-value"}
	bound := NewBoundSecrets(reader, mappings)
	mappings["alias"] = SecretBinding{Target: target, Env: "RUN_VALUE", Service: "substituted", Key: "other"}
	req := Request{Job: Job{Target: target}}
	ref := EnvReference{Env: "RUN_VALUE", Ref: "alias"}
	got, err := bound.Resolve(context.Background(), req, ref)
	if err != nil || got != reader.value || reader.service != "permitted" || reader.key != "key" {
		t.Fatal("binding substituted", got, err)
	}
	reader.value = "rotated-fake-value"
	got, err = bound.Resolve(context.Background(), req, ref)
	if err != nil || got != reader.value {
		t.Fatal("runtime value rotation failed", got, err)
	}
	req.Job.Target.ID = "other"
	if _, err = bound.Resolve(context.Background(), req, ref); err == nil {
		t.Fatal("alias became target authority")
	}
}
func TestFailureEpisodeThroughExplicitEngineNotifiesOnce(t *testing.T) {
	clock := &testClock{at: time.Now().UTC()}
	sink := &notificationFixture{idem: true}
	state := ErrExecutionFailed
	c, _ := testCore(t, executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
		if err := admit(ctx, "plan", func() error { return nil }); err != nil {
			return err
		}
		return state
	}), allowAt(clock), Options{Clock: clock, Notifications: sink})
	j := dueJob(clock, PipelineRun)
	j.Timing = Timing{Interval: time.Second}
	mustCreate(t, c, j)
	for i := 0; i < 3; i++ {
		clock.advance(time.Second)
		tick(t, c)
	}
	if sink.calls != 1 {
		t.Fatal("failure episode sent repeatedly", sink.calls)
	}
	state = nil
	clock.advance(time.Second)
	tick(t, c)
	if sink.calls != 2 {
		t.Fatal("missing recovery", sink.calls)
	}
	state = errors.New("ambiguous transport outcome")
	clock.advance(time.Second)
	tick(t, c)
	if sink.calls != 2 {
		t.Fatal("unknown outcome notified as failure", sink.calls)
	}
}
