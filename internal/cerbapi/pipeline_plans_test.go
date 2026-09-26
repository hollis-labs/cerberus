package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// shellPipeline is a one-stage pipeline whose shell action touches marker
// in dir, next to a resource action on svc.
func shellPipeline(dir, marker string) config.PipelineDef {
	return config.PipelineDef{ID: "ship", Name: "Ship", Stages: []config.StageDef{{Name: "one", Actions: []config.ActionDef{
		{Type: "shell", Command: "touch " + marker, Dir: dir},
		{Type: "stop", Resource: "svc"},
	}}}}
}

func setPipelines(svc *ResourceRuntimeService, resources []config.ResourceDef, pipelines ...config.PipelineDef) {
	svc.cfgMu.Lock()
	svc.cfg = &config.ConfigV2{Resources: resources, Pipelines: pipelines}
	svc.cfgMu.Unlock()
}

// A pipeline plan names every action and digests the pipeline and the
// resources it names; asked for on its own it runs nothing, is recorded as
// a dry run, and is the plan an approval of the run binds to.
func TestPipelinePlanOnRequest(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	dir := t.TempDir()
	svc := NewResourceRuntimeService(sink)
	setPipelines(svc, []config.ResourceDef{devResource(t, nil)}, shellPipeline(dir, "ran"))
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	shown, err := svc.RunPipeline(ctx, "ship", WithAcknowledged(true), WithPlan())
	if err != nil || shown.Plan == nil {
		t.Fatalf("plan: %+v %v", shown, err)
	}
	p := shown.Plan.Plan
	if p.Lane != plan.LanePipeline || len(p.Steps) != 2 || p.Steps[0].Command != "touch ran" || p.Steps[0].Dir != dir ||
		p.Steps[1].Command != "stop svc" || p.Digests["pipeline"] == "" || p.Digests["resource:svc"] == "" {
		t.Fatalf("plan %+v", p)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "ran")); statErr == nil {
		t.Fatal("asking for a plan ran the pipeline")
	}
	if o := outcome(sink.Records()); !o.DryRun {
		t.Fatalf("not recorded as a dry run: %+v", o)
	}
	_, err = svc.RunPipeline(ctx, "ship", WithAcknowledged(true))
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil {
		t.Fatalf("asking: %v", err)
	}
	if a, _ := broker.Get(coded.Approval.ID); a.PlanHash != shown.Plan.PlanHash {
		t.Fatalf("approval binds %s, plan shown %s", a.PlanHash, shown.Plan.PlanHash)
	}
}

// An approved run uses its approval; an edited stage or an edited resource
// the pipeline names is plan_stale, and nothing runs.
func TestPipelineRunUsesItsApproval(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	// A pipeline's target is unlabeled, so out of band: a test verifier
	// stands in for the presence proof.
	broker.SetPresenceVerifier(acceptPresence{})
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	ask := func(svc *ResourceRuntimeService) string {
		t.Helper()
		_, err := svc.RunPipeline(ctx, "ship", WithAcknowledged(true))
		var coded *ExternalConnectorError
		if !errors.As(err, &coded) || coded.Approval == nil {
			t.Fatalf("asking: %v", err)
		}
		if _, err := broker.Decide(ctx, coded.Approval.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Via: "cli"}}); err != nil {
			t.Fatal(err)
		}
		return coded.Approval.ID
	}
	for name, change := range map[string]func(string, config.ResourceDef) ([]config.ResourceDef, config.PipelineDef){
		"edited stage": func(dir string, r config.ResourceDef) ([]config.ResourceDef, config.PipelineDef) {
			p := shellPipeline(dir, "ran")
			p.Stages[0].Actions[0].Command = "touch other"
			return []config.ResourceDef{r}, p
		},
		"edited resource": func(dir string, r config.ResourceDef) ([]config.ResourceDef, config.PipelineDef) {
			r.Config["command"] = []string{"sleep", "61"}
			return []config.ResourceDef{r}, shellPipeline(dir, "ran")
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			svc := NewResourceRuntimeService(sink)
			res := devResource(t, nil)
			setPipelines(svc, []config.ResourceDef{res}, shellPipeline(dir, "ran"))
			id := ask(svc)
			resources, changed := change(dir, res)
			setPipelines(svc, resources, changed)
			if _, err := svc.RunPipeline(ctx, "ship", WithAcknowledged(true), WithApprovalID(id)); connectorErrorCode(err) != ExternalConnectorPlanStale {
				t.Fatalf("err = %v", err)
			}
			for _, marker := range []string{"ran", "other"} {
				if _, statErr := os.Stat(filepath.Join(dir, marker)); statErr == nil {
					t.Fatal("a stale approval ran the pipeline")
				}
			}
		})
	}
	t.Run("unchanged", func(t *testing.T) {
		dir := t.TempDir()
		svc := NewResourceRuntimeService(sink)
		setPipelines(svc, []config.ResourceDef{devResource(t, nil)}, shellPipeline(dir, "ran"))
		id := ask(svc)
		out, err := svc.RunPipeline(ctx, "ship", WithAcknowledged(true), WithApprovalID(id))
		if err != nil || !out.Success {
			t.Fatalf("approved run: %+v %v", out, err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "ran")); statErr != nil {
			t.Fatal("the approved run did not run")
		}
		if o := outcome(sink.Records()); o.ApprovalID != id || o.PlanHash == "" {
			t.Fatalf("outcome %+v", o)
		}
	})
}

// A run given the snapshot its plan was checked against executes that
// snapshot, not the config as it reads by then.
func TestPipelineRunsTheCheckedSnapshot(t *testing.T) {
	dir := t.TempDir()
	svc := NewResourceRuntimeService(audit.NewMemory())
	res := devResource(t, nil)
	setPipelines(svc, []config.ResourceDef{res}, shellPipeline(dir, "now"))
	checked := &pipelineSnapshot{def: shellPipeline(dir, "checked"), resources: []config.ResourceDef{res}}
	checked.def.Stages[0].Actions = checked.def.Stages[0].Actions[:1]
	if out, err := svc.runPipeline(context.Background(), "ship", checked, WithAcknowledged(true)); err != nil || !out.Success {
		t.Fatalf("run: %+v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "checked")); err != nil {
		t.Fatal("the checked snapshot did not run")
	}
	if _, err := os.Stat(filepath.Join(dir, "now")); err == nil {
		t.Fatal("the run read the config again")
	}
}

// A pipeline plan over the socket takes run/plan: a current daemon answers
// with the plan, and one that predates plans refuses without running.
func TestPipelinePlanRoute(t *testing.T) {
	dir := t.TempDir()
	svc := NewResourceRuntimeService(audit.NewMemory())
	setPipelines(svc, []config.ResourceDef{devResource(t, nil)}, shellPipeline(dir, "ran"))
	client := startConnectorSocket(t, NewInProcessClient(WithResourceRuntimeService(svc)))
	out, err := client.RunPipeline(context.Background(), "ship", WithAcknowledged(true), WithPlan())
	if err != nil || out.Plan == nil {
		t.Fatalf("plan over the socket: %+v %v", out, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "ran")); statErr == nil {
		t.Fatal("the plan route ran the pipeline")
	}

	sock := filepath.Join(tempSocketDir(t), "old.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/pipelines/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/pipelines/"), "/", 2)
		if parts[1] != "run" {
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown pipeline action %q", parts[1]))
			return
		}
		ran.Add(1)
		writeJSON(w, http.StatusOK, PipelineRunResult{Success: true})
	})
	srv := &http.Server{Handler: mux} //nolint:gosec // a test server on a temp unix socket
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	_, err = NewSocketClient(sock).RunPipeline(context.Background(), "ship", WithAcknowledged(true), WithPlan())
	if err == nil || !strings.Contains(err.Error(), "predates plans") || ran.Load() != 0 {
		t.Fatalf("old daemon: %v, ran %d", err, ran.Load())
	}
}
