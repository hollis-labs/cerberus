package cerbapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/oauth"
	"github.com/hollis-labs/cerberus/internal/scheduling"
	"github.com/hollis-labs/libs/util/scheduler"
)

func TestScheduleSignedNamespacesAndAdmin(t *testing.T) {
	core, _ := scheduleFixture(t)
	grants := scheduleSignedHost(t, []ScheduleGrant{
		{Subject: "client:a", App: "a", MaxJobs: 2},
		{Subject: "client:b", App: "b", MaxJobs: 2},
		{Subject: "client:admin", App: "admin", MaxJobs: 2, AdminView: true},
	})
	sink := audit.NewMemory()
	service := NewScheduleService(core, sink, grants)
	path := startPeerSocket(t, NewInProcessClient(WithScheduleService(service)), nil)
	tokens := map[string]string{}
	clients := map[string]*SocketClient{}
	for _, app := range []string{"a", "b", "admin"} {
		token, _ := scheduleToken(t, app, []string{oauth.ScopeOperate})
		tokens[app] = token
		clients[app] = NewSocketClient(path, WithBearer(func(context.Context) string { return token }))
	}
	// HTTP uses the real socket auth path, carrying the private signed token.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := NewSocketClient(path, WithBearer(func(context.Context) string { return r.Header.Get("Authorization") }))
		ScheduleHTTP(client, "/schedules/v1/").ServeHTTP(w, r)
	}))
	defer server.Close()
	callHTTP := func(app string, call scheduling.Call) scheduling.Result {
		t.Helper()
		body, err := json.Marshal(call)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, server.URL+"/schedules/v1/call", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", tokens[app])
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var out scheduleEnvelope
		if err = json.NewDecoder(response.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || out.Error != nil || out.Result == nil {
			t.Fatalf("HTTP %d: %+v", response.StatusCode, out)
		}
		return *out.Result
	}
	for _, app := range []string{"a", "b"} {
		j := fixtureJob()
		j.OwnerApp = app
		callHTTP(app, scheduling.Call{Operation: "register", OwnerApp: app, IdempotencyKey: "startup", Registration: []scheduling.Registration{{Job: j, Absent: true}}, Acknowledged: true})
		out := callHTTP(app, scheduling.Call{Operation: "list"})
		if len(out.Jobs) != 1 || out.Jobs[0].Job.OwnerApp != app {
			t.Fatalf("leaked list: %+v", out)
		}
		other := "a"
		if app == "a" {
			other = "b"
		}
		for _, op := range []string{"get", "list", "update", "pause", "resume", "delete", "history", "logs", "run_now", "dry_run", "create", "register"} {
			r := scheduling.Call{Operation: op, OwnerApp: other, ID: "job", Revision: "claimed", FireID: "guessed", Limit: 10, RequestID: "manual", IdempotencyKey: "attack", Acknowledged: true}
			if op == "create" || op == "update" || op == "dry_run" {
				foreign := j
				foreign.OwnerApp = other
				r.Job = &foreign
			}
			if op == "register" {
				foreign := j
				foreign.OwnerApp = other
				r.Registration = []scheduling.Registration{{Job: foreign, Absent: true}}
			}
			assertScheduleCode(context.Background(), t, clients[app], r, "forbidden")
		}
		assertScheduleCode(context.Background(), t, clients[app], scheduling.Call{Operation: "admin_view", Acknowledged: true}, "forbidden")
	}
	all := callHTTP("admin", scheduling.Call{Operation: "admin_view", Acknowledged: true})
	if len(all.Jobs) != 2 {
		t.Fatal(all)
	}
	adminAudited := false
	for _, record := range sink.Records() {
		if record.Operation == "admin_view" && record.Effect == "admin" {
			adminAudited = true
		}
	}
	if !adminAudited {
		t.Fatal("admin view bypassed shared audit")
	}
	assertScheduleCode(context.Background(), t, clients["admin"], scheduling.Call{Operation: "delete", OwnerApp: "a", ID: "job", Revision: all.Jobs[0].Revision, Acknowledged: true}, "forbidden")
	// Same UID and fabricated exported verified labels cannot mint private proof.
	forged := WithPrincipal(BeginRequest(context.Background(), SurfaceSocket), Principal{UIDVerified: true, AuthMethod: AuthOAuth, Issuer: ProcessAuth().Config.BuiltinIssuer(), Subject: "client:a", Scopes: []string{oauth.ScopeOperate}})
	assertScheduleCode(forged, t, service, scheduling.Call{Operation: "list"}, "forbidden")
	assertScheduleCode(context.Background(), t, NewSocketClient(path), scheduling.Call{Operation: "list", OwnerApp: "a"}, "forbidden")
	noGrant := NewScheduleService(core, sink)
	assertScheduleCode(scheduleTokenContext(t, tokens["a"]), t, noGrant, scheduling.Call{Operation: "list"}, "unavailable")
	readToken, _ := scheduleToken(t, "admin", []string{oauth.ScopeRead})
	assertScheduleCode(scheduleTokenContext(t, readToken), t, service, scheduling.Call{Operation: "admin_view", Acknowledged: true}, string(ExternalConnectorInsufficientScope))
}

func TestScheduleExpiryDuringRealVerificationRefuses(t *testing.T) {
	_, service := scheduleFixture(t)
	auth := ProcessAuth()
	barrier := &scheduleVerifierBarrier{TokenVerifier: auth.Verifier, at: 4, entered: make(chan struct{}), release: make(chan struct{})}
	auth.Verifier = barrier
	token, _, err := auth.Issuer.Issue("fixture", []string{oauth.ScopeRead}, 2*time.Second, audit.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := scheduleTokenContext(t, token)
	proof := ctx.Value(bearerProofKey{}).(*bearerProof)
	done := make(chan error, 1)
	go func() { _, e := service.Schedule(ctx, scheduling.Call{Operation: "list"}); done <- e }()
	<-barrier.entered
	<-time.After(time.Until(proof.identity.Expiry) + 25*time.Millisecond)
	close(barrier.release)
	if err = <-done; scheduling.ErrorCode(err) != "forbidden" {
		t.Fatal("expired during verification", err)
	}
}

type schedulePrivateExecutor func(context.Context, scheduling.Target, scheduling.Admission) error

func (f schedulePrivateExecutor) Execute(ctx context.Context, target scheduling.Target, admit scheduling.Admission) error {
	return f(ctx, target, admit)
}

type schedulePrivateAuthority func(context.Context, scheduling.Request) (*scheduling.Permit, error)

func (f schedulePrivateAuthority) Authorize(ctx context.Context, r scheduling.Request) (*scheduling.Permit, error) {
	return f(ctx, r)
}

type schedulePrivateResolver func(context.Context, scheduling.Request, scheduling.EnvReference) (string, error)

func (f schedulePrivateResolver) Resolve(ctx context.Context, r scheduling.Request, ref scheduling.EnvReference) (string, error) {
	return f(ctx, r, ref)
}

type schedulePrivateDeliveryExecutor struct{ schedulePrivateExecutor }

func (f schedulePrivateDeliveryExecutor) ExecuteDelivery(ctx context.Context, target scheduling.Target, admit scheduling.Admission, _ *scheduling.Delivery) error {
	return f.Execute(ctx, target, admit)
}

func TestScheduleManualAuthorizationAndResolutionRechecks(t *testing.T) {
	for _, stage := range []string{"authority", "runtime-recheck", "resolver"} {
		t.Run(stage, func(t *testing.T) {
			grants := scheduleSignedHost(t, []ScheduleGrant{{Subject: "client:fixture", App: "fixture", MaxJobs: 4}})
			token, id := scheduleToken(t, "fixture", []string{oauth.ScopeOperate})
			ctx := scheduleTokenContext(t, token)
			revoke := func() {
				if _, err := ProcessAuth().Issuer.Store.Revoke(id); err != nil {
					t.Fatal(err)
				}
			}
			authority := schedulePrivateAuthority(func(ctx context.Context, r scheduling.Request) (*scheduling.Permit, error) {
				if stage == "authority" {
					revoke()
				}
				return (scheduledTestAuthority{}).Authorize(ctx, r)
			})
			resolver := schedulePrivateResolver(func(context.Context, scheduling.Request, scheduling.EnvReference) (string, error) {
				revoke()
				return "private-synthetic-secret", nil
			})
			sends := 0
			executor := schedulePrivateDeliveryExecutor{schedulePrivateExecutor(func(ctx context.Context, _ scheduling.Target, admit scheduling.Admission) error {
				err := admit(ctx, "private-plan", func() error {
					if stage == "runtime-recheck" {
						revoke()
					}
					return nil
				})
				if err != nil {
					return err
				}
				sends++
				return nil
			})}
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "manual.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			core, err := scheduling.New(context.Background(), db, executor, authority, scheduling.Options{Delivery: scheduling.DeliveryOptions{Secrets: resolver}})
			if err != nil {
				t.Fatal(err)
			}
			service := NewScheduleService(core, audit.NewMemory(), grants)
			job := fixtureJob()
			job.Target.Kind = scheduling.PipelineRun
			if stage == "resolver" {
				job.EnvRefs = []scheduling.EnvReference{{Env: "RUN_VALUE", Ref: "private-ref"}}
			}
			if _, err = service.Schedule(ctx, scheduling.Call{Operation: "create", Job: &job, IdempotencyKey: "job", Acknowledged: true}); err != nil {
				t.Fatal(err)
			}
			assertScheduleCode(ctx, t, service, scheduling.Call{Operation: "run_now", OwnerApp: job.OwnerApp, ID: job.ID, RequestID: "once", Acknowledged: true}, "forbidden")
			if sends != 0 {
				t.Fatal("revoked manual fire sent")
			}
			fires, err := core.History(context.Background(), job.OwnerApp, job.ID, 1)
			if err != nil || len(fires) != 1 {
				t.Fatal(fires, err)
			}
			receipt, found, err := core.Receipt(context.Background(), fires[0].ID)
			if err != nil || !found || receipt.State != scheduling.Failed {
				t.Fatal("pre-send refusal receipt", receipt, found, err)
			}
		})
	}
}

func TestScheduleManualCapacityWaitRevocationNeverSends(t *testing.T) {
	for _, change := range []string{"revoke", "grant"} {
		t.Run(change, func(t *testing.T) {
			grants := scheduleSignedHost(t, []ScheduleGrant{{Subject: "client:fixture", App: "fixture", MaxJobs: 4}})
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "manual.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			entered := make(chan struct{})
			release := make(chan struct{})
			var secondSends atomic.Int32
			executor := schedulePrivateExecutor(func(ctx context.Context, target scheduling.Target, admit scheduling.Admission) error {
				if admitErr := admit(ctx, "private-plan", func() error { return nil }); admitErr != nil {
					return admitErr
				}
				if target.ID == "first" {
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				secondSends.Add(1)
				return nil
			})
			core, err := scheduling.New(context.Background(), db, executor, scheduledTestAuthority{}, scheduling.Options{Concurrency: 1, MaxFireTimeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			service := NewScheduleService(core, audit.NewMemory(), grants)
			first := fixtureJob()
			first.ID = "first"
			first.Target.ID = "first"
			first.Timeout = 5 * time.Second
			second := first
			second.ID = "second"
			second.Target.ID = "second"
			token, _ := scheduleToken(t, "fixture", []string{oauth.ScopeOperate})
			firstCtx := scheduleTokenContext(t, token)
			secondToken, secondID := scheduleToken(t, "fixture", []string{oauth.ScopeOperate})
			secondCtx := scheduleTokenContext(t, secondToken)
			for _, job := range []scheduling.Job{first, second} {
				if _, err = service.Schedule(firstCtx, scheduling.Call{Operation: "create", Job: &job, IdempotencyKey: job.ID, Acknowledged: true}); err != nil {
					t.Fatal(err)
				}
			}
			firstDone := make(chan error, 1)
			go func() {
				_, e := service.Schedule(firstCtx, scheduling.Call{Operation: "run_now", OwnerApp: "fixture", ID: "first", RequestID: "first", Acknowledged: true})
				firstDone <- e
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first did not occupy capacity")
			}
			secondCall := scheduling.Call{Operation: "run_now", OwnerApp: "fixture", ID: "second", RequestID: "second", Acknowledged: true}
			secondDone := make(chan error, 1)
			go func() { _, e := service.Schedule(secondCtx, secondCall); secondDone <- e }()
			deadline := time.After(3 * time.Second)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
		waiting:
			for {
				select {
				case <-ticker.C:
					fires, e := core.History(context.Background(), "fixture", "second", 10)
					if e != nil {
						t.Fatal(e)
					}
					if len(fires) == 1 && fires[0].Status == scheduler.FireClaimed {
						break waiting
					}
				case <-deadline:
					t.Fatal("second did not claim durable fire before capacity wait")
				}
			}
			if change == "revoke" {
				if _, err = ProcessAuth().Issuer.Store.Revoke(secondID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = grants.Replace([]ScheduleGrant{{Issuer: ProcessAuth().Config.BuiltinIssuer(), Subject: "client:fixture", App: "fixture", MaxJobs: 4}}); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			if err = <-secondDone; scheduling.ErrorCode(err) != "forbidden" {
				t.Fatal("waiting caller did not refuse", err)
			}
			if secondSends.Load() != 0 {
				t.Fatal("revoked waiting caller sent effect")
			}
			<-firstDone // A changed grant can refuse disclosure after the first effect was admitted.
			freshToken, _ := scheduleToken(t, "fixture", []string{oauth.ScopeOperate})
			freshCtx := scheduleTokenContext(t, freshToken)
			replay, err := service.Schedule(freshCtx, secondCall)
			if err != nil {
				t.Fatal(err)
			}
			if replay.Run == nil || replay.Run.Fire.Status != scheduler.FireExhausted || secondSends.Load() != 0 {
				t.Fatal("refused fire replayed", replay)
			}
		})
	}
}

func TestScheduleBlockedFinalClaimReadRechecksProof(t *testing.T) {
	for _, change := range []string{"revoke", "grant", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			input := []ScheduleGrant{{Subject: "client:fixture", App: "fixture", MaxJobs: 4}}
			grants := scheduleSignedHost(t, input)
			auth := ProcessAuth()
			barrier := &scheduleVerifierBarrier{TokenVerifier: auth.Verifier, at: 10, entered: make(chan struct{}), release: make(chan struct{})}
			auth.Verifier = barrier
			token, id := scheduleToken(t, "fixture", []string{oauth.ScopeOperate})
			ctx := scheduleTokenContext(t, token)
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "final-claim.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			var sends atomic.Int32
			executor := schedulePrivateExecutor(func(ctx context.Context, _ scheduling.Target, admit scheduling.Admission) error {
				if admitErr := admit(ctx, "plan", func() error { return nil }); admitErr != nil {
					return admitErr
				}
				sends.Add(1)
				return nil
			})
			core, err := scheduling.New(context.Background(), db, executor, scheduledTestAuthority{}, scheduling.Options{})
			if err != nil {
				t.Fatal(err)
			}
			service := NewScheduleService(core, audit.NewMemory(), grants)
			job := fixtureJob()
			job.Timeout = 10 * time.Second
			if _, err = service.Schedule(ctx, scheduling.Call{Operation: "create", Job: &job, IdempotencyKey: "job", Acknowledged: true}); err != nil {
				t.Fatal(err)
			}
			barrier.calls.Store(0)
			done := make(chan error, 1)
			go func() {
				_, e := service.Schedule(ctx, scheduling.Call{Operation: "run_now", OwnerApp: job.OwnerApp, ID: job.ID, RequestID: "once", Acknowledged: true})
				done <- e
			}()
			select {
			case <-barrier.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("send commit verification did not block")
			}
			baseline := db.Stats().WaitCount
			holder := make(chan *sql.Conn, 1)
			holderErr := make(chan error, 1)
			go func() {
				conn, e := db.Conn(context.Background())
				if e != nil {
					holderErr <- e
					return
				}
				holder <- conn
			}()
			waitCount := func(want int64) {
				t.Helper()
				deadline := time.After(5 * time.Second)
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for db.Stats().WaitCount < want {
					select {
					case <-ticker.C:
					case <-deadline:
						t.Fatal("SQL connection wait not reached")
					}
				}
			}
			waitCount(baseline + 1) // Queue a competing connection while the send transaction owns it.
			close(barrier.release)
			var held *sql.Conn
			select {
			case held = <-holder:
			case e := <-holderErr:
				t.Fatal(e)
			case <-time.After(5 * time.Second):
				t.Fatal("competing connection did not acquire")
			}
			waitCount(baseline + 2) // The fresh final claim read is now waiting for that connection.
			switch change {
			case "revoke":
				if _, err = auth.Issuer.Store.Revoke(id); err != nil {
					t.Fatal(err)
				}
			case "grant":
				input[0].Issuer = auth.Config.BuiltinIssuer()
				if err = grants.Replace(input); err != nil {
					t.Fatal(err)
				}
			}
			if err = held.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("final claim request did not finish")
			}
			expected := int32(0)
			if change == "unchanged" {
				expected = 1
				if err != nil {
					t.Fatal(err)
				}
			} else if scheduling.ErrorCode(err) != "forbidden" {
				t.Fatal(err)
			}
			if sends.Load() != expected {
				t.Fatalf("after claim wait sends=%d want=%d", sends.Load(), expected)
			}
			fires, e := core.History(context.Background(), job.OwnerApp, job.ID, 1)
			if e != nil || len(fires) != 1 {
				t.Fatal(fires, e)
			}
			receipt, found, e := core.Receipt(context.Background(), fires[0].ID)
			want := scheduling.Unknown
			if change == "unchanged" {
				want = scheduling.Completed
			}
			if e != nil || !found || receipt.State != want {
				t.Fatal("send-CAS receipt boundary", receipt, found, e)
			}
		})
	}
}

// The barrier delays a real verifier; it cannot mint identities or grants.
type scheduleVerifierBarrier struct {
	TokenVerifier
	calls   atomic.Int32
	at      int32
	entered chan struct{}
	release chan struct{}
}

func (v *scheduleVerifierBarrier) Verify(ctx context.Context, raw string) (oauth.Identity, error) {
	if v.calls.Add(1) == v.at {
		close(v.entered)
		select {
		case <-v.release:
		case <-ctx.Done():
			return oauth.Identity{}, ctx.Err()
		}
	}
	return v.TokenVerifier.Verify(ctx, raw)
}
func TestScheduleRevocationAndGrantEpochAfterWaits(t *testing.T) {
	for _, change := range []string{"revoke", "grant"} {
		for _, stage := range []int32{3, 4, 5, 6} {
			t.Run(fmt.Sprintf("%s-check-%d", change, stage), func(t *testing.T) {
				core, _ := scheduleFixture(t)
				input := []ScheduleGrant{{Issuer: ProcessAuth().Config.BuiltinIssuer(), Subject: "client:fixture", App: "fixture", MaxJobs: 2}}
				grants, err := NewScheduleGrants(input)
				if err != nil {
					t.Fatal(err)
				}
				auth := ProcessAuth()
				barrier := &scheduleVerifierBarrier{TokenVerifier: auth.Verifier, at: stage, entered: make(chan struct{}), release: make(chan struct{})}
				auth.Verifier = barrier
				token, id := scheduleToken(t, "fixture", []string{oauth.ScopeOperate})
				ctx := scheduleTokenContext(t, token)
				service := NewScheduleService(core, audit.NewMemory(), grants)
				j := fixtureJob()
				result := make(chan error, 1)
				go func() {
					out, e := service.Schedule(ctx, scheduling.Call{Operation: "create", Job: &j, IdempotencyKey: "waiting", Acknowledged: true})
					if e != nil && out.Job != nil {
						result <- errors.New("refused request disclosed job")
						return
					}
					result <- e
				}()
				select {
				case <-barrier.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("guard checkpoint not reached")
				}
				if change == "revoke" {
					if _, err = auth.Issuer.Store.Revoke(id); err != nil {
						t.Fatal(err)
					}
				} else {
					if err = grants.Replace(input); err != nil {
						t.Fatal(err)
					}
				}
				close(barrier.release)
				select {
				case err = <-result:
				case <-time.After(5 * time.Second):
					t.Fatal("guard request did not finish")
				}
				if scheduling.ErrorCode(err) != "forbidden" {
					t.Fatal(err)
				}
				_, found, err := core.Get(context.Background(), j.OwnerApp, j.ID)
				if err != nil {
					t.Fatal(err)
				}
				if found != (stage == 6) {
					t.Fatalf("checkpoint%d persisted=%v", stage, found)
				}
			})
		}
	}
}

func TestScheduleProofRechecksAndCopiedGrants(t *testing.T) {
	core, _ := scheduleFixture(t)
	input := []ScheduleGrant{{Issuer: ProcessAuth().Config.BuiltinIssuer(), Subject: "client:fixture", App: "fixture", MaxJobs: 1}}
	grants, err := NewScheduleGrants(input)
	if err != nil {
		t.Fatal(err)
	}
	input[0].App = "stolen"
	service := NewScheduleService(core, audit.NewMemory(), grants)
	token, id := scheduleToken(t, "fixture", []string{oauth.ScopeOperate})
	ctx := scheduleTokenContext(t, token)
	if _, err = service.Schedule(ctx, scheduling.Call{Operation: "list"}); err != nil {
		t.Fatal(err)
	}
	// Exported principal tampering cannot widen actual token scopes.
	readToken, _ := scheduleToken(t, "fixture", []string{oauth.ScopeRead})
	readCtx := scheduleTokenContext(t, readToken)
	p, _ := PrincipalFrom(readCtx)
	p.Scopes = []string{oauth.ScopeOperate}
	readCtx = WithPrincipal(readCtx, p)
	j := fixtureJob()
	assertScheduleCode(readCtx, t, service, scheduling.Call{Operation: "create", Job: &j, IdempotencyKey: "tamper", Acknowledged: true}, string(ExternalConnectorInsufficientScope))
	if _, err = ProcessAuth().Issuer.Store.Revoke(id); err != nil {
		t.Fatal(err)
	}
	assertScheduleCode(ctx, t, service, scheduling.Call{Operation: "list"}, "forbidden")
	replacement := *ProcessAuth()
	SetAuth(&replacement)
	assertScheduleCode(readCtx, t, service, scheduling.Call{Operation: "list"}, "forbidden")
	// Missing/expired real signed proof refuses even though JWT verifier allows leeway.
	expiredToken, _, err := replacement.Issuer.Issue("fixture", []string{oauth.ScopeRead}, time.Nanosecond, audit.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	expiredCtx := scheduleTokenContext(t, expiredToken)
	assertScheduleCode(expiredCtx, t, service, scheduling.Call{Operation: "list"}, "forbidden")
}
