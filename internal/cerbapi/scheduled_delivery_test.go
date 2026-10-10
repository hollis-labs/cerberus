package cerbapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	gmcp "github.com/hollis-labs/libs/plugin-mcp/go-mcp/server"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/scheduling"
	"github.com/hollis-labs/libs/util/scheduler"
)

func TestScheduledCallerArgvInjectionRejectedBeforeService(t *testing.T) {
	for _, principal := range []PrincipalKind{PrincipalHuman, PrincipalAgent} {
		t.Run(string(principal), func(t *testing.T) {
			probe := &scheduleBodyProbe{}
			handler := ScheduleHTTP(probe, "/schedules/v1/")
			ctx := WithPrincipal(BeginRequest(context.Background(), SurfaceSocket), Principal{Kind: principal, UIDVerified: true, Via: ViaCLI})
			for _, body := range []string{
				`{"operation":"run_now","owner_app":"test-app","id":"configured","request_id":"once","argv":["caller-command"]}`,
				`{"operation":"create","job":{"id":"configured","argv":["caller-command"]}}`,
				`{"operation":"create","job":{"target":{"kind":"pipeline_run","id":"configured","argv":["caller-command"]}}}`,
			} {
				req := httptest.NewRequest(http.MethodPost, "/schedules/v1/call", strings.NewReader(body)).WithContext(ctx)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusBadRequest || probe.calls != 0 {
					t.Fatalf("caller argv reached service: status=%d calls=%d", rec.Code, probe.calls)
				}
			}
			// Prove the handler routes a valid selector-only request; rejection
			// above is about injected execution data, not an inert test handler.
			req := httptest.NewRequest(http.MethodPost, "/schedules/v1/call", strings.NewReader(`{"operation":"run_now","owner_app":"test-app","id":"configured","request_id":"once"}`)).WithContext(ctx)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || probe.calls != 1 {
				t.Fatal("selector-only control did not reach service", rec.Code, probe.calls)
			}
		})
	}
}

// This test-owned subprocess uses portable argv, never a live model/connector.
func TestScheduleDeliveryChild(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "schedule-child" {
		return
	}
	value := os.Getenv("RUN_VALUE")
	if value == "" {
		os.Exit(31)
	}
	for i := 1; i < len(value); i++ {
		_, _ = fmt.Fprint(os.Stdout, value[:i])
		_, _ = fmt.Fprintln(os.Stdout, value[i:])
	}
	_, _ = fmt.Fprint(os.Stderr, strings.Repeat("child-noise-", 20000)+value)
	os.Exit(7)
}

type scheduleResolver struct {
	calls  int
	value  string
	before func()
}

func (r *scheduleResolver) Resolve(context.Context, scheduling.Request, scheduling.EnvReference) (string, error) {
	r.calls++
	if r.before != nil {
		r.before()
	}
	return r.value, nil
}

func TestScheduledRealPipelineEnvironmentAndSeparateStreams(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withPDP(t, constantPDP{decision: policy.Allow})
	sink := audit.NewMemory()
	const sentinel = "fake-runtime-private-value-271828"
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	def := config.PipelineDef{ID: "capture", Name: "Capture", Stages: []config.StageDef{{Name: sentinel, Actions: []config.ActionDef{{Type: "shell", Argv: []string{exe, "-test.run=^TestScheduleDeliveryChild$", "--", "schedule-child"}, Dir: t.TempDir()}}}}}
	runtime := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(&config.ConfigV2{Pipelines: []config.PipelineDef{def}}))
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	resolver := &scheduleResolver{value: sentinel}
	core, err := scheduling.New(context.Background(), db, ScheduledExecutor{Runtime: runtime}, scheduledTestAuthority{}, scheduling.Options{Delivery: scheduling.DeliveryOptions{Secrets: resolver, StreamBytes: 2048}})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	job := scheduling.Job{ID: "capture", Name: "Capture", OwnerApp: "test-app", Target: scheduling.Target{Kind: scheduling.PipelineRun, ID: "capture"}, Timing: scheduling.Timing{At: time.Now()}, Timeout: 5 * time.Second, Enabled: true, CaptureLogs: true, EnvRefs: []scheduling.EnvReference{{Env: "RUN_VALUE", Ref: "test-secret"}}}
	if err = core.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 0 {
		t.Fatal("creation resolved a secret")
	}
	notices := []gmcp.Notification{}
	runCtx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) { notices = append(notices, n) })
	if err = core.TickNow(runCtx); err != nil {
		t.Fatal(err)
	}
	fire, found, err := core.Fire(context.Background(), scheduler.DeriveFireID(job.Key(), job.Timing.At))
	if err != nil || !found {
		t.Fatal(found, err)
	}
	receipt, found, err := core.Receipt(context.Background(), fire.ID)
	if err != nil || !found || receipt.State != scheduling.Failed {
		t.Fatal("real child exit did not become confirmed failure", receipt, err)
	}
	logs, err := core.Logs(context.Background(), job.OwnerApp, job.ID, fire.ID)
	if err != nil || !logs.Available || !logs.Truncated || !strings.Contains(logs.Stdout, "[REDACTED]") || !strings.Contains(logs.Stderr, "child-noise") {
		t.Fatalf("streams not captured: %+v %v", logs, err)
	}
	if strings.Contains(logs.Stdout+logs.Stderr+receipt.Error+fire.LastError, sentinel) {
		t.Fatal("secret escaped stored output")
	}
	var raw string
	for _, query := range []string{`SELECT group_concat(payload) FROM gosched_schedules`, `SELECT group_concat(error) FROM cerberus_schedule_receipts`, `SELECT group_concat(last_error) FROM gosched_fires`, `SELECT group_concat(body) FROM cerberus_schedule_logs`} {
		if err = db.QueryRow(query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, sentinel) {
			t.Fatal("secret in SQL storage")
		}
	}
	encodedNotices, err := json.Marshal(notices)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedNotices), sentinel) {
		t.Fatal("secret label escaped progress transport")
	}
	for _, record := range sink.Records() {
		if strings.Contains(fmt.Sprint(record), sentinel) {
			t.Fatal("secret in audit")
		}
	}
	service := NewScheduleService(core, sink)
	request := scheduling.Call{Operation: "logs", OwnerApp: job.OwnerApp, ID: job.ID, FireID: fire.ID}
	if _, err = service.Schedule(context.Background(), request); scheduling.ErrorCode(err) != "forbidden" {
		t.Fatal("unbound log read accepted", err)
	}
	ctx := WithPrincipal(BeginRequest(context.Background(), SurfaceSocket), Principal{Kind: PrincipalHuman, UIDVerified: true, Via: ViaCLI})
	out, err := service.Schedule(ctx, request)
	if err != nil || !out.LogsAvailable {
		t.Fatal("authorized log read failed", err)
	}
}
func TestScheduledDeliveryUnsupportedPipelineNeverResolvesOrRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withPDP(t, constantPDP{decision: policy.Allow})
	runtime := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(&config.ConfigV2{Pipelines: []config.PipelineDef{{ID: "mixed", Stages: []config.StageDef{{Name: "stop", Actions: []config.ActionDef{{Type: "stop", Resource: "missing"}}}}}}}))
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	resolver := &scheduleResolver{value: "fake-private-long-value"}
	core, err := scheduling.New(context.Background(), db, ScheduledExecutor{Runtime: runtime}, scheduledTestAuthority{}, scheduling.Options{Delivery: scheduling.DeliveryOptions{Secrets: resolver}})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	job := scheduling.Job{ID: "mixed", Name: "Mixed", OwnerApp: "test-app", Target: scheduling.Target{Kind: scheduling.PipelineRun, ID: "mixed"}, Timing: scheduling.Timing{At: time.Now()}, Timeout: time.Second, Enabled: true, CaptureLogs: true, EnvRefs: []scheduling.EnvReference{{Env: "RUN_VALUE", Ref: "alias"}}}
	if err = core.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err = core.TickNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	fire, _, err := core.Fire(context.Background(), scheduler.DeriveFireID(job.Key(), job.Timing.At))
	if err != nil || resolver.calls != 0 || !strings.Contains(fire.LastError, "unavailable") {
		t.Fatal("unsupported pipeline resolved/runs", resolver.calls, fire.LastError, err)
	}
}
func TestScheduledArgvSnapshotAndPlanBindActualCommand(t *testing.T) {
	def := config.PipelineDef{ID: "one", Stages: []config.StageDef{{Actions: []config.ActionDef{{Type: "shell", Argv: []string{"test-program", "old"}}}}}}
	snapshot := clonePipelineDefinition(def)
	def.Stages[0].Actions[0].Argv[1] = "changed"
	if snapshot.Stages[0].Actions[0].Argv[1] != "old" {
		t.Fatal("argv snapshot aliases source")
	}
}

func TestScheduledSecretResolutionRechecksPolicyAndArgv(t *testing.T) {
	for _, change := range []string{"policy", "argv"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			withPDP(t, constantPDP{decision: policy.Allow})
			sink := audit.NewMemory()
			cfg := &config.ConfigV2{Pipelines: []config.PipelineDef{{ID: "one", Name: "One", Stages: []config.StageDef{{Name: "shell", Actions: []config.ActionDef{{Type: "shell", Argv: []string{"test-owned-must-never-run"}}}}}}}}
			runtime := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(cfg))
			resolver := &scheduleResolver{value: "synthetic-long-secret", before: func() {
				if change == "policy" {
					SetPolicyDecisionPoint(constantPDP{decision: policy.Deny})
				} else {
					cfg.Pipelines[0].Stages[0].Actions[0].Argv[0] = "substituted-must-never-run"
				}
			}}
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			core, err := scheduling.New(context.Background(), db, ScheduledExecutor{Runtime: runtime}, scheduledTestAuthority{}, scheduling.Options{Delivery: scheduling.DeliveryOptions{Secrets: resolver}})
			if err != nil {
				t.Fatal(err)
			}
			defer core.Stop()
			job := scheduling.Job{ID: "one", Name: "One", OwnerApp: "test-app", Target: scheduling.Target{Kind: scheduling.PipelineRun, ID: "one"}, Timing: scheduling.Timing{At: time.Now()}, Timeout: time.Second, Enabled: true, CaptureLogs: true, EnvRefs: []scheduling.EnvReference{{Env: "RUN_VALUE", Ref: "alias"}}}
			if err = core.Create(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if err = core.TickNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			fire, _, err := core.Fire(context.Background(), scheduler.DeriveFireID(job.Key(), job.Timing.At))
			if err != nil {
				t.Fatal(err)
			}
			receipt, _, err := core.Receipt(context.Background(), fire.ID)
			if err != nil || resolver.calls != 1 || receipt.PlanHash != "" || receipt.State != scheduling.Failed {
				t.Fatal("changed binding admitted effect", receipt, err)
			}
			logs, err := core.Logs(context.Background(), job.OwnerApp, job.ID, fire.ID)
			if err != nil || logs.Available {
				t.Fatal("refused execution claimed captured output", logs, err)
			}
		})
	}
}
