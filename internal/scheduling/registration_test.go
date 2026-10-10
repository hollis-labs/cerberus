package scheduling

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRegistrationPreservesEditsAndIncarnation(t *testing.T) {
	c, clock := testCore(t, nil, nil, Options{})
	s := NewService(c, fixtureAccess)
	j := dueJob(clock, ResourceStart)
	j.Timing = Timing{Interval: time.Hour}
	req := Call{Operation: "register", OwnerApp: j.OwnerApp, IdempotencyKey: "startup", Registration: []Registration{{Job: j, Absent: true}}}
	first := callOK(t, s, req).Jobs[0]
	paused := callOK(t, s, Call{Operation: "pause", OwnerApp: j.OwnerApp, ID: j.ID, Revision: first.Revision}).Job
	edited := j
	edited.Name = "Operator edit"
	edited.Enabled = false
	current := callOK(t, s, Call{Operation: "update", OwnerApp: j.OwnerApp, ID: j.ID, Revision: paused.Revision, Job: &edited}).Job
	for _, key := range []string{"startup", "next-startup"} {
		req.IdempotencyKey = key
		repeated := callOK(t, s, req).Jobs[0]
		if repeated.Revision != current.Revision || repeated.NextRun != current.NextRun || repeated.Job.Name != "Operator edit" || repeated.State != "paused" {
			t.Fatalf("startup overwrote state: %+v", repeated)
		}
	}
	changed := req
	changed.IdempotencyKey = "upgrade"
	changed.Registration = append([]Registration(nil), req.Registration...)
	changed.Registration[0].Job.Name = "New desired"
	callCode(t, s, changed, "conflict")
	changed.Registration[0].Absent = false
	changed.Registration[0].Revision = first.Revision
	callCode(t, s, changed, "conflict")
	changed.Registration[0].Revision = current.Revision
	upgraded := callOK(t, s, changed).Jobs[0]
	if upgraded.Job.Incarnation != first.Job.Incarnation || upgraded.Job.Generation <= current.Job.Generation || upgraded.Job.Enabled {
		t.Fatal("upgrade reset incarnation/pause", upgraded)
	}
	// Changing a previously used request key cannot write a new desired definition.
	changed.IdempotencyKey = "startup"
	callCode(t, s, changed, "conflict")
	callOK(t, s, Call{Operation: "delete", OwnerApp: j.OwnerApp, ID: j.ID, Revision: upgraded.Revision})
	req.IdempotencyKey = "recreate"
	recreated := callOK(t, s, req).Jobs[0]
	if recreated.Job.Incarnation == first.Job.Incarnation {
		t.Fatal("recreate reused incarnation")
	}
	req.IdempotencyKey = "startup"
	callCode(t, s, req, "conflict")
}
func TestRegistrationAtomicQuotaAndConcurrentAdmission(t *testing.T) {
	c, clock := testCore(t, nil, nil, Options{})
	c.db.SetMaxOpenConns(4)
	s := NewService(c, fixtureAccess)
	j := dueJob(clock, ResourceStart)
	j.Timing = Timing{Interval: time.Hour}
	ctx := WithAccessCheck(context.Background(), func(context.Context) error { return nil }, 1)
	second := j
	second.ID = "second"
	batch := Call{Operation: "register", OwnerApp: j.OwnerApp, IdempotencyKey: "batch", Registration: []Registration{{Job: j, Absent: true}, {Job: second, Absent: true}}}
	if _, err := s.Schedule(ctx, batch); ErrorCode(err) != "quota_exceeded" {
		t.Fatal(err)
	}
	if jobs, err := c.List(ctx, j.OwnerApp); err != nil || len(jobs) != 0 {
		t.Fatal("partial batch persisted", jobs, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			item := j
			item.ID = fmt.Sprintf("job-%d", i)
			_, err := s.Schedule(ctx, Call{Operation: "create", Job: &item, IdempotencyKey: item.ID})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if ErrorCode(err) != "quota_exceeded" {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("quota admitted %d", success)
	}
}
func TestRegistrationPreconditionsAndNoOmittedDeletion(t *testing.T) {
	c, clock := testCore(t, nil, nil, Options{})
	s := NewService(c, fixtureAccess)
	j := dueJob(clock, ResourceStart)
	other := j
	other.ID = "omitted"
	callOK(t, s, Call{Operation: "create", Job: &other, IdempotencyKey: "omitted"})
	r := Call{Operation: "register", OwnerApp: j.OwnerApp, IdempotencyKey: "one", Registration: []Registration{{Job: j}}}
	callCode(t, s, r, "invalid")
	r.Registration[0].Absent = true
	r.Registration[0].Revision = "both"
	callCode(t, s, r, "invalid")
	r.Registration[0].Revision = ""
	callOK(t, s, r)
	if _, found, err := c.Get(context.Background(), other.OwnerApp, other.ID); err != nil || !found {
		t.Fatal("omitted job deleted", err)
	}
	r.IdempotencyKey = "duplicates"
	r.Registration = append(r.Registration, r.Registration[0])
	callCode(t, s, r, "invalid")
}

func TestManualPermitExpiresAtSendBoundary(t *testing.T) {
	for _, checkpoint := range []int{3, 4, 5} {
		t.Run(fmt.Sprintf("guard-%d", checkpoint), func(t *testing.T) {
			clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
			entered := make(chan struct{})
			release := make(chan struct{})
			active := false
			checks := 0
			sends := 0
			authority := authorityFunc(func(_ context.Context, r Request) (*Permit, error) {
				p := testPermit(r, clock.Now())
				p.ExpiresAt = clock.Now().Add(time.Second)
				return p, nil
			})
			executor := executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
				active = true
				if err := admit(ctx, "plan", func() error { return nil }); err != nil {
					return err
				}
				sends++
				return nil
			})
			c, _ := testCore(t, executor, authority, Options{Clock: clock})
			s := NewService(c, fixtureAccess)
			job := dueJob(clock, ResourceStart)
			job.Timing = Timing{Interval: time.Hour}
			job.Timeout = 10 * time.Second
			callOK(t, s, Call{Operation: "create", Job: &job, IdempotencyKey: "job"})
			ctx := WithAccessCheck(context.Background(), func(context.Context) error {
				if active {
					checks++
					if checks == checkpoint {
						close(entered)
						<-release
					}
				}
				return nil
			}, 8)
			done := make(chan error, 1)
			go func() {
				_, err := s.Schedule(ctx, Call{Operation: "run_now", OwnerApp: job.OwnerApp, ID: job.ID, RequestID: "one"})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("send boundary not reached")
			}
			clock.advance(2 * time.Second)
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			} // Dispatch refusal is a durable run result.
			if sends != 0 {
				t.Fatal("expired permit sent an effect")
			}
			fires, err := c.History(context.Background(), job.OwnerApp, job.ID, 1)
			if err != nil || len(fires) != 1 {
				t.Fatal(fires, err)
			}
			receipt, found, err := c.Receipt(context.Background(), fires[0].ID)
			if err != nil || !found {
				t.Fatal(receipt, found, err)
			}
			want := Unknown
			if checkpoint == 3 {
				want = Failed
			}
			if receipt.State != want {
				t.Fatalf("receipt=%s want=%s", receipt.State, want)
			}
			callOK(t, s, Call{Operation: "run_now", OwnerApp: job.OwnerApp, ID: job.ID, RequestID: "one"})
			if sends != 0 {
				t.Fatal("expired permit replayed")
			}
		})
	}
}

func TestManualClaimLeaseExpiresDuringFinalAccess(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			clock := &testClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
			entered := make(chan struct{})
			release := make(chan struct{})
			active := false
			checks := 0
			sends := 0
			executor := executorFunc(func(ctx context.Context, _ Target, admit Admission) error {
				active = true
				if err := admit(ctx, "plan", func() error { return nil }); err != nil {
					return err
				}
				sends++
				return nil
			})
			c, _ := testCore(t, executor, allowAt(clock), Options{Clock: clock})
			s := NewService(c, fixtureAccess)
			job := dueJob(clock, ResourceStart)
			job.Timing = Timing{Interval: time.Hour}
			job.Timeout = 10 * time.Second
			callOK(t, s, Call{Operation: "create", Job: &job, IdempotencyKey: "job"})
			ctx := WithAccessCheck(context.Background(), func(context.Context) error {
				if active {
					checks++
					if checks == 5 {
						close(entered)
						<-release
					}
				}
				return nil
			}, 8)
			done := make(chan error, 1)
			go func() {
				_, e := s.Schedule(ctx, Call{Operation: "run_now", OwnerApp: job.OwnerApp, ID: job.ID, RequestID: "once"})
				done <- e
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("final access did not block")
			}
			advance := time.Second
			if expired {
				advance = 31 * time.Second
			}
			clock.advance(advance)
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			expected := 1
			want := Completed
			if expired {
				expected = 0
				want = Sent
			}
			if sends != expected {
				t.Fatalf("sends=%d want=%d", sends, expected)
			}
			fires, err := c.History(context.Background(), job.OwnerApp, job.ID, 1)
			if err != nil || len(fires) != 1 {
				t.Fatal(fires, err)
			}
			receipt, found, err := c.Receipt(context.Background(), fires[0].ID)
			if err != nil || !found || receipt.State != want {
				t.Fatal(receipt, found, err)
			}
		})
	}
}
