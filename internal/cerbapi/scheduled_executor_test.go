package cerbapi

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/pipeline"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/scheduling"
	"github.com/hollis-labs/libs/util/scheduler"
	_ "modernc.org/sqlite"
)

func fakeScheduledSpec(kind scheduling.TargetKind) auditSpec {
	def, opname := localconn.Definition(), localconn.OpApply
	if kind == scheduling.ResourceDeploy {
		opname = localconn.OpDeploy
	}
	if kind == scheduling.PipelineRun {
		def, opname = pipeline.Definition(), pipeline.OpRun
	}
	op, known := def.Operation(opname)
	spec := auditSpec{connector: def.ID, operation: opname, op: op, known: known, acknowledged: true, config: map[string]any{"id": "target"}}
	spec.plan = func(context.Context) (plan.Plan, error) {
		return plan.Plan{Lane: "fake", Connector: def.ID, Operation: opname, Digests: map[string]string{"spec": "test-current-spec"}}, nil
	}
	return spec
}

func TestScheduledRuntimeGatesRemainEnforced(t *testing.T) {
	for _, kind := range []scheduling.TargetKind{scheduling.ResourceStart, scheduling.ResourceDeploy, scheduling.PipelineRun} {
		for _, scenario := range []string{"allow", "deny-in-shadow", "approval", "lockdown", "lockdown-during-authorize", "missing-admission", "wrong-target", "plan-changed-during-authorize"} {
			t.Run(string(kind)+"/"+scenario, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				decision := policy.Allow
				if scenario == "deny-in-shadow" {
					decision = policy.Deny
				}
				if scenario == "approval" {
					decision = policy.Approve
				}
				withPDP(t, constantPDP{decision: decision})
				store := withBrakes(t)
				sink := audit.NewMemory()
				ctx := BeginRequest(WithPrincipal(context.Background(), confirmHuman), SurfaceScheduler)
				p, ok := PrincipalFrom(ctx)
				if !ok || p.Kind != PrincipalAutomation || p.ActingFor != PrincipalAgent || CallerSurfaceFrom(ctx) != SurfaceScheduler {
					t.Fatalf("scheduler inherited caller authority: %+v", p)
				}
				if scenario == "lockdown" {
					if _, _, err := EngageLockdown(ctx, sink, store, "test incident"); err != nil {
						t.Fatal(err)
					}
				}
				var sent bool
				spec := fakeScheduledSpec(kind)
				planRevision := "original"
				spec.plan = func(context.Context) (plan.Plan, error) {
					return plan.Plan{Lane: "fake", Digests: map[string]string{"spec": planRevision}}, nil
				}
				admit := func(effectCtx context.Context, hash string, recheck func() error) error {
					if hash == "" {
						t.Fatal("effect not bound to current plan")
					}
					if scenario == "lockdown-during-authorize" {
						if _, _, err := EngageLockdown(effectCtx, sink, store, "changed during authorization"); err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "plan-changed-during-authorize" {
						planRevision = "changed"
					}
					if err := recheck(); err != nil {
						return err
					}
					sent = true
					return nil
				}
				if scenario != "missing-admission" {
					target := scheduling.Target{Kind: kind, ID: "target"}
					if scenario == "wrong-target" {
						target.ID = "other"
					}
					ctx = context.WithValue(ctx, scheduledAdmissionKey{}, scheduledAdmission{target: target, admit: admit})
				}
				call, err := beginGated(ctx, sink, slog.Default(), spec)
				if err == nil {
					err = admitScheduledEffect(ctx, spec, call)
					call.finish(err)
				}
				if scenario == "allow" {
					if err != nil || !sent {
						t.Fatalf("authorized fake effect refused: %v", err)
					}
				} else if err == nil || sent {
					t.Fatalf("%s allowed scheduled effect: %v", scenario, err)
				}
				if scenario != "allow" {
					o := outcome(sink.Records())
					if o.Decision != audit.DecisionRefused {
						t.Fatalf("refusal not audited: %+v", o)
					}
				}
			})
		}
	}
}

func TestScheduledAdapterChecksAfterResourceAndPipelineLocks(t *testing.T) {
	for _, kind := range []scheduling.TargetKind{scheduling.ResourceStart, scheduling.ResourceDeploy, scheduling.PipelineRun} {
		t.Run(string(kind), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			withPDP(t, constantPDP{decision: policy.Allow})
			sink := audit.NewMemory()
			runtime := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(&config.ConfigV2{
				Resources: []config.ResourceDef{{ID: "target", Type: "process", Connector: "local", Config: map[string]any{"mode": "dev_session", "dir": t.TempDir(), "command": []any{"test-owned-unused-command"}}}},
				Pipelines: []config.PipelineDef{{ID: "target", Name: "Fake", Stages: []config.StageDef{{Name: "fake", Actions: []config.ActionDef{{Type: "shell", Command: "exit 7"}}}}}},
			}))
			var unlock func()
			if kind == scheduling.PipelineRun {
				runtime.pipelineMu.Lock()
				unlock = runtime.pipelineMu.Unlock
			} else {
				runtime.opMu.Lock()
				unlock = runtime.opMu.Unlock
			}
			admitted := make(chan struct{}, 1)
			refused := errors.New("test refuses at admission, before any connector execution")
			done := make(chan error, 1)
			go func() {
				done <- (ScheduledExecutor{Runtime: runtime}).Execute(WithPrincipal(context.Background(), confirmHuman), scheduling.Target{Kind: kind, ID: "target"}, func(context.Context, string, func() error) error { admitted <- struct{}{}; return refused })
			}()
			select {
			case <-admitted:
				unlock()
				t.Fatal("permit evaluated before runtime lock")
			case <-time.After(20 * time.Millisecond):
			}
			unlock()
			if err := <-done; !errors.Is(err, refused) {
				t.Fatalf("expected admission refusal: %v", err)
			}
			<-admitted
			if o := outcome(sink.Records()); o.Principal.Surface != "scheduler" || o.Principal.Kind != audit.PrincipalAutomation || o.Decision != audit.DecisionRefused {
				t.Fatalf("scheduler audit: %+v", o)
			}
		})
	}
}

type scheduledTestAuthority struct{}

func (scheduledTestAuthority) Authorize(_ context.Context, r scheduling.Request) (*scheduling.Permit, error) {
	return &scheduling.Permit{JobKey: r.Job.Key(), Revision: r.Revision, FireID: r.FireID, Target: r.Job.Target, PlanHash: r.PlanHash, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

// The fake pipeline's only action exits with an error in a test-owned working
// directory; it calls no connector or live service. Transport success must not
// turn that execution failure into succeeded dispatch history.
func TestScheduledPipelineFailureReachesFireHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withPDP(t, constantPDP{decision: policy.Allow})
	sink := audit.NewMemory()
	runtime := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(&config.ConfigV2{Pipelines: []config.PipelineDef{{ID: "fake", Name: "Fake failure", Stages: []config.StageDef{{Name: "fake", Actions: []config.ActionDef{{Type: "shell", Command: "exit 7", Dir: t.TempDir()}}}}}}}))
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	core, err := scheduling.New(context.Background(), db, ScheduledExecutor{Runtime: runtime}, scheduledTestAuthority{}, scheduling.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	job := scheduling.Job{ID: "failure", Name: "Failure", OwnerApp: "fake-app", Target: scheduling.Target{Kind: scheduling.PipelineRun, ID: "fake"}, Timing: scheduling.Timing{At: time.Now()}, Timeout: time.Second, Enabled: true}
	if createErr := core.Create(context.Background(), job); createErr != nil {
		t.Fatal(createErr)
	}
	if tickErr := core.TickNow(context.Background()); tickErr != nil {
		t.Fatal(tickErr)
	}
	fire, found, fireErr := core.Fire(context.Background(), scheduler.DeriveFireID(job.Key(), job.Timing.At))
	if fireErr != nil || !found || fire.Status != scheduler.FireExhausted {
		t.Fatalf("failed pipeline dispatch: %+v found=%v err=%v", fire, found, fireErr)
	}
	receipt, found, receiptErr := core.Receipt(context.Background(), fire.ID)
	if receiptErr != nil || !found || receipt.State != scheduling.Failed {
		t.Fatalf("failed pipeline receipt: %+v found=%v err=%v", receipt, found, receiptErr)
	}
	if record := outcome(sink.Records()); record.OutcomeCode != string(ExternalConnectorOperationFailed) || record.PlanHash == "" {
		t.Fatalf("failed pipeline audit: %+v", record)
	}
}

func TestScheduledMissingCallerSurfaceFailsClosed(t *testing.T) {
	ctx := context.WithValue(context.Background(), scheduledAdmissionKey{}, scheduledAdmission{target: scheduling.Target{Kind: scheduling.ResourceStart, ID: "target"}, admit: func(context.Context, string, func() error) error {
		t.Fatal("unmarked context invoked admission")
		return nil
	}})
	if err := admitScheduledEffect(ctx, fakeScheduledSpec(scheduling.ResourceStart), nil); err == nil {
		t.Fatal("missing caller surface admitted effect")
	}
}

func TestScheduledTargetDefinitionChangeRefusesBeforeSend(t *testing.T) {
	for _, kind := range []scheduling.TargetKind{scheduling.ResourceStart, scheduling.ResourceDeploy, scheduling.PipelineRun} {
		t.Run(string(kind), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			withPDP(t, constantPDP{decision: policy.Allow})
			sink := audit.NewMemory()
			runtime := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(&config.ConfigV2{
				Resources: []config.ResourceDef{{ID: "target", Type: "process", Connector: "local", Config: map[string]any{"mode": "dev_session", "dir": t.TempDir(), "command": []any{"test-owned-unused-command"}}}},
				Pipelines: []config.PipelineDef{{ID: "target", Name: "Original", Stages: []config.StageDef{{Name: "fake", Actions: []config.ActionDef{{Type: "shell", Command: "exit 7"}}}}}},
			}))
			err := (ScheduledExecutor{Runtime: runtime}).Execute(context.Background(), scheduling.Target{Kind: kind, ID: "target"}, func(_ context.Context, _ string, recheck func() error) error {
				runtime.cfgMu.Lock()
				runtime.cfg = &config.ConfigV2{}
				runtime.cfgMu.Unlock()
				if checkErr := recheck(); checkErr != nil {
					return checkErr
				}
				t.Fatal("changed definition admitted an effect")
				return errors.New("test refuses connector execution")
			})
			var coded *ExternalConnectorError
			if !errors.As(err, &coded) || coded.Code != ExternalConnectorPlanStale {
				t.Fatalf("changed target did not refuse as stale: %v", err)
			}
		})
	}
}
