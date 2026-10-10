package scheduling

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/util/scheduler"
)

func fixtureAccess(context.Context, Call) (func(error), error) { return func(error) {}, nil }
func callOK(t *testing.T, s Service, r Call) Result {
	t.Helper()
	out, err := s.Schedule(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func callCode(t *testing.T, s Service, r Call, code string) {
	t.Helper()
	_, err := s.Schedule(context.Background(), r)
	if ErrorCode(err) != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func TestServiceDurableCRUDIdempotencyAndFences(t *testing.T) {
	c, clock := testCore(t, nil, nil, Options{})
	s := NewService(c, fixtureAccess)
	j := dueJob(clock, ResourceStart)
	j.Timing = Timing{Interval: time.Hour, Location: "America/Chicago"}
	create := Call{Operation: "create", Job: &j, IdempotencyKey: "create-1"}
	v := callOK(t, s, create).Job
	if v.Job.Generation == 0 {
		t.Fatal("missing server generation")
	}
	duplicate := callOK(t, s, create).Job
	if duplicate.Revision != v.Revision {
		t.Fatal("create replay changed revision")
	}
	changed := j
	changed.Name = "Changed"
	callCode(t, s, Call{Operation: "create", Job: &changed, IdempotencyKey: "create-1"}, "conflict")
	base := Call{OwnerApp: j.OwnerApp, ID: j.ID, Revision: v.Revision}
	base.Operation = "pause"
	paused := callOK(t, s, base).Job
	if paused.State != "paused" || paused.Revision == v.Revision {
		t.Fatal("pause did not fence revision")
	}
	base.Operation = "resume"
	callCode(t, s, base, "conflict")
	base.Revision = paused.Revision
	resumed := callOK(t, s, base).Job
	if resumed.Revision == v.Revision {
		t.Fatal("pause/resume ABA")
	}
	list := callOK(t, s, Call{Operation: "list", OwnerApp: j.OwnerApp, State: "enabled"})
	if len(list.Jobs) != 1 {
		t.Fatal(list)
	}
	if len(callOK(t, s, Call{Operation: "list", OwnerApp: "other"}).Jobs) != 0 {
		t.Fatal("app filter")
	}
	base.Operation = "delete"
	base.Revision = resumed.Revision
	callOK(t, s, base)
	callCode(t, s, create, "conflict")
	oldCreate := create
	create.IdempotencyKey = "create-2"
	newJob := j
	newJob.Target.ID = "different-target"
	create.Job = &newJob
	recreated := callOK(t, s, create).Job
	oldCreate.IdempotencyKey = "create-1"
	callCode(t, s, oldCreate, "conflict")
	if recreated.Revision == v.Revision {
		t.Fatal("delete/recreate ABA")
	}
	if recreated.Job.Timing != j.Timing || recreated.NextRun != clock.Now().Add(time.Hour) {
		t.Fatal("published options roundtrip")
	}
}
func TestServiceConcurrentCreateAndEdits(t *testing.T) {
	c, clock := testCore(t, nil, nil, Options{})
	s := NewService(c, fixtureAccess)
	j := dueJob(clock, ResourceStart)
	req := Call{Operation: "create", Job: &j, IdempotencyKey: "same"}
	var wg sync.WaitGroup
	out := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			v, e := s.Schedule(context.Background(), req)
			if e != nil {
				out <- e.Error()
				return
			}
			out <- v.Job.Revision
		})
	}
	wg.Wait()
	close(out)
	rev := ""
	for v := range out {
		if rev == "" {
			rev = v
		}
		if v != rev {
			t.Fatal("non-atomic create", v, rev)
		}
	}
	errorsOut := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			_, err := s.Schedule(context.Background(), Call{Operation: "pause", OwnerApp: j.OwnerApp, ID: j.ID, Revision: rev})
			errorsOut <- err
		})
	}
	wg.Wait()
	close(errorsOut)
	success, conflict := 0, 0
	for e := range errorsOut {
		if e == nil {
			success++
		} else if ErrorCode(e) == "conflict" {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("CAS: %d %d", success, conflict)
	}
}
func TestServiceDryRunZonesAndUnavailableLogs(t *testing.T) {
	c, clock := testCore(t, nil, nil, Options{})
	s := NewService(c, fixtureAccess)
	j := dueJob(clock, ResourceStart)
	j.Timing = Timing{Cron: "0 9 * * *", Location: "America/Chicago"}
	times := callOK(t, s, Call{Operation: "dry_run", Job: &j, Limit: 3}).Times
	if len(times) != 3 || times[0].Hour() != 14 {
		t.Fatal(times)
	}
	j.Timing.Location = "Missing/Zone"
	callCode(t, s, Call{Operation: "dry_run", Job: &j, Limit: 3}, "invalid")
	j.Timing = Timing{At: clock.Now().Add(time.Minute)}
	if len(callOK(t, s, Call{Operation: "dry_run", Job: &j, Limit: 3}).Times) != 1 {
		t.Fatal("one off repeats")
	}
	callCode(t, NewService(c, nil), Call{Operation: "list"}, "unavailable")
	callCode(t, s, Call{Operation: "run_now", OwnerApp: j.OwnerApp, ID: j.ID, RequestID: "run-1"}, "forbidden")
}
func TestEditsDuringClaimRefuseBeforeAdmission(t *testing.T) {
	for _, op := range []string{"update", "pause", "delete"} {
		t.Run(op, func(t *testing.T) {
			clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
			entered := make(chan struct{})
			release := make(chan struct{})
			sent := 0
			executor := executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
				close(entered)
				<-release
				if err := admit(ctx, "plan", func() error { return nil }); err != nil {
					return err
				}
				sent++
				return nil
			})
			c, _ := testCore(t, executor, allowAt(clock), Options{Clock: clock})
			s := NewService(c, fixtureAccess)
			j := dueJob(clock, ResourceStart)
			v := callOK(t, s, Call{Operation: "create", Job: &j, IdempotencyKey: "create"}).Job
			done := make(chan error, 1)
			go func() { done <- c.TickNow(context.Background()) }()
			<-entered
			req := Call{Operation: op, OwnerApp: j.OwnerApp, ID: j.ID, Revision: v.Revision}
			if op == "update" {
				j.Name = "Edited"
				req.Job = &j
			}
			callOK(t, s, req)
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if sent != 0 {
				t.Fatal("stale claimed fire sent")
			}
			f := fireFor(t, c, j, clock.Now())
			if f.Status != scheduler.FireExhausted {
				t.Fatal(f)
			}
		})
	}
}
func TestRunNowSelectedFireDuplicateAndUnknown(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "unknown"}[ambiguous], func(t *testing.T) {
			clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
			sent := 0
			executor := executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
				if err := admit(ctx, "plan", func() error { return nil }); err != nil {
					return err
				}
				sent++
				if ambiguous {
					return errors.New("transport lost")
				}
				return nil
			})
			c, _ := testCore(t, executor, allowAt(clock), Options{Clock: clock})
			s := NewService(c, fixtureAccess)
			j := dueJob(clock, ResourceStart)
			j.Timing = Timing{Interval: time.Hour}
			v := callOK(t, s, Call{Operation: "create", Job: &j, IdempotencyKey: "create"}).Job
			other := j
			other.ID = "unrelated"
			other.Timing = Timing{At: clock.Now()}
			callOK(t, s, Call{Operation: "create", Job: &other, IdempotencyKey: "other"})
			req := Call{Operation: "run_now", OwnerApp: j.OwnerApp, ID: j.ID, RequestID: "run-1"}
			run := callOK(t, s, req).Run
			again := callOK(t, s, req).Run
			if run.Fire.ID != again.Fire.ID || sent != 1 {
				t.Fatalf("manual replay or unrelated dispatch sent=%d run=%+v receipt=%+v again=%+v", sent, run.Fire, run.Receipt, again.Fire)
			}
			want := Completed
			if ambiguous {
				want = Unknown
			}
			if run.Receipt == nil || run.Receipt.State != want {
				t.Fatal(run)
			}
			current := callOK(t, s, Call{Operation: "get", OwnerApp: j.OwnerApp, ID: j.ID}).Job
			if current.NextRun != v.NextRun {
				t.Fatal("manual changed recurrence")
			}
			logs := callOK(t, s, Call{Operation: "logs", OwnerApp: j.OwnerApp, ID: j.ID, FireID: run.Fire.ID})
			if logs.LogsAvailable || logs.LogsReason == "" {
				t.Fatal("invented logs")
			}
			callCode(t, s, Call{Operation: "logs", OwnerApp: other.OwnerApp, ID: other.ID, FireID: run.Fire.ID}, "not_found")
		})
	}
}

func TestSharedManualAndEngineConcurrencyBound(t *testing.T) {
	for _, otherEngine := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual-manual", true: "manual-engine"}[otherEngine], func(t *testing.T) {
			clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
			entered := make(chan Target, 2)
			release := make(chan struct{}, 2)
			defer close(release)
			executor := executorFunc(func(ctx context.Context, target Target, admit Admission) error {
				if err := admit(ctx, "plan", func() error { return nil }); err != nil {
					return err
				}
				entered <- target
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			c, _ := testCore(t, executor, allowAt(clock), Options{Clock: clock, Concurrency: 1})
			s := NewService(c, fixtureAccess)
			first := dueJob(clock, ResourceStart)
			first.Timing = Timing{Interval: time.Hour}
			first.Timeout = 10 * time.Second
			second := first
			second.ID = "second"
			second.Target.ID = "second"
			if otherEngine {
				second.Timing = Timing{At: clock.Now()}
			}
			callOK(t, s, Call{Operation: "create", Job: &first, IdempotencyKey: "first"})
			callOK(t, s, Call{Operation: "create", Job: &second, IdempotencyKey: "second"})
			done := make(chan error, 2)
			go func() {
				_, err := s.Schedule(context.Background(), Call{Operation: "run_now", OwnerApp: first.OwnerApp, ID: first.ID, RequestID: "first"})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("first did not enter")
			}
			if otherEngine {
				go func() { done <- c.TickNow(context.Background()) }()
			} else {
				go func() {
					_, err := s.Schedule(context.Background(), Call{Operation: "run_now", OwnerApp: second.OwnerApp, ID: second.ID, RequestID: "second"})
					done <- err
				}()
			}
			select {
			case <-entered:
				t.Fatal("global concurrency bound bypassed")
			case <-time.After(50 * time.Millisecond):
			}
			release <- struct{}{}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("second never admitted after release")
			}
			release <- struct{}{}
			for i := 0; i < 2; i++ {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCreateIdempotencyAcrossGrowingPoolsAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pools.db")
	first := openTestDB(t, path)
	second := openTestDB(t, path)
	first.SetMaxOpenConns(4)
	second.SetMaxOpenConns(4)
	one, err := New(context.Background(), first, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	two, err := New(context.Background(), second, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	services := []Service{NewService(one, fixtureAccess), NewService(two, fixtureAccess)}
	j := Job{ID: "pool", OwnerApp: "fixture", Name: "Fixture", Timing: Timing{Interval: time.Hour}, Target: Target{Kind: ResourceStart, ID: "fake"}, Enabled: true, Timeout: time.Second}
	req := Call{Operation: "create", Job: &j, IdempotencyKey: "same"}
	results := make(chan string, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		service := services[i%2]
		wg.Go(func() {
			out, callErr := service.Schedule(context.Background(), req)
			if callErr != nil {
				results <- callErr.Error()
				return
			}
			results <- out.Job.Revision
		})
	}
	wg.Wait()
	close(results)
	revision := ""
	for got := range results {
		if revision == "" {
			revision = got
		}
		if got != revision {
			t.Fatalf("concurrent pools diverged %s / %s", revision, got)
		}
	}
	reopened, err := New(context.Background(), openTestDB(t, path), nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := callOK(t, NewService(reopened, fixtureAccess), req); got.Job.Revision != revision {
		t.Fatal("create receipt did not survive reopen")
	}
}
