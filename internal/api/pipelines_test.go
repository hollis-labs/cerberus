package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/service"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/supervise"
	"github.com/hollis-labs/cerberus/internal/target"
)

// The pipeline routes end to end through the real lane, as main's pipeline_plans_test.go,
// pipeline_service_test.go, brakes_test.go and baseline_enforced_test.go exercised them.

// pipelineWorld is the API over a lane whose config the test can change between calls.
type pipelineWorld struct {
	*gatedAPI
	mu  sync.Mutex
	cfg *config.ConfigV2
	dir string
}

func (p *pipelineWorld) set(resources []config.ResourceDef, pipelines ...config.PipelineDef) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = &config.ConfigV2{Resources: resources, Pipelines: pipelines}
}

func (p *pipelineWorld) ran(marker string) bool {
	_, err := os.Stat(filepath.Join(p.dir, marker))
	return err == nil
}

func newPipelineWorld(t *testing.T, o gatetest.Options, mode settings.Mode) *pipelineWorld {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", base)
	p := &pipelineWorld{dir: t.TempDir()}
	ctx := context.Background()
	scfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: filepath.Join(base, "cerberus.db"), CreateParentDir: true}}
	st, err := store.Open(ctx, scfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fsys, root := storage.Migrations()
	if err = migrate.Ensure(ctx, scfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
		t.Fatal(err)
	}
	key, err := audit.OpenDigester(filepath.Join(base, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	service.SetStateRoot(base)
	t.Cleanup(func() { service.SetStateRoot("") })
	w := gatetest.New(t, o)
	readCfg := func() (*config.ConfigV2, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.cfg, nil
	}
	lane, err := supervise.New(supervise.Options{Mode: mode, Home: base, StateDir: base, Store: st, Digester: key, Gate: w.Gate, Config: readCfg})
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Resources: func() ([]config.ResourceDef, error) {
		c, _ := readCfg()
		return c.Resources, nil
	}, Observer: fixedObserver{}, Home: base, Pipelines: lane}
	if mode == settings.ModeSupervise {
		opts.Gate = w.Gate
	}
	a, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	p.gatedAPI = &gatedAPI{h: a.Mux(), w: w}
	return p
}

// svc is a dev-session resource a pipeline stops; shellPipeline touches marker in dir next to
// that stop (main's fixtures).
func svcResource(labels ...func(*config.ResourceDef)) config.ResourceDef {
	r := config.ResourceDef{ID: "svc", Type: "process", Project: "demo", Connector: "local",
		Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf},
		Config: map[string]any{"mode": "dev_session", "command": []any{"/bin/sleep", "60"}}}
	for _, f := range labels {
		f(&r)
	}
	return r
}

func shellPipeline(dir, marker string) config.PipelineDef {
	return config.PipelineDef{ID: "ship", Name: "Ship", Stages: []config.StageDef{{Name: "one", Actions: []config.ActionDef{
		{Type: "shell", Command: "touch " + marker, Dir: dir},
		{Type: "stop", Resource: "svc"},
	}}}}
}

// The list and a definition are main's shapes; an invalid definition stays inspectable with
// its validation error, and an unknown id answers null, as the old socket did.
func TestPipelineListAndDetail(t *testing.T) {
	p := newPipelineWorld(t, gatetest.Options{}, settings.ModeSupervise)
	broken := config.PipelineDef{ID: "broken", Name: "Broken", Stages: []config.StageDef{{Name: "one", Actions: []config.ActionDef{{Type: "stop", Resource: "missing"}}}}}
	p.set([]config.ResourceDef{svcResource()}, shellPipeline(p.dir, "ran"), broken)

	code, body := p.call(t, apiKey, "GET", "/api/pipelines", "")
	if code != 200 || !strings.Contains(body, `{"id":"ship","name":"Ship","stage_count":1}`) || !strings.Contains(body, `"id":"broken"`) {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, body = p.call(t, apiKey, "GET", "/api/pipelines/ship", ""); code != 200 || !strings.Contains(body, `"definition":{`) || strings.Contains(body, "validation_error") {
		t.Fatalf("detail: %d %s", code, body)
	}
	if code, body = p.call(t, apiKey, "GET", "/api/pipelines/broken", ""); code != 200 || !strings.Contains(body, `"validation_error":"resolve pipeline: `) {
		t.Fatalf("invalid detail: %d %s", code, body)
	}
	if code, body = p.call(t, apiKey, "GET", "/api/pipelines/nope", ""); code != 200 || strings.TrimSpace(body) != "null" {
		t.Fatalf("unknown detail: %d %s", code, body)
	}
	if p.ran("ran") {
		t.Fatal("reading a pipeline ran it")
	}
}

// A run needs the acknowledgment, and is recorded as pipeline.run; with it, the stages run and
// the answer carries the executor's result. A plan request runs nothing, is recorded as a dry
// run, and names every action, digesting the pipeline and the resources it names, with the
// stop's own resource plan.
func TestPipelineRunAndPlan(t *testing.T) {
	p := newPipelineWorld(t, gatetest.Options{}, settings.ModeSupervise)
	p.set([]config.ResourceDef{svcResource()}, shellPipeline(p.dir, "ran"))

	if code, body := p.call(t, apiKey, "POST", "/api/pipelines/ship/run", `{}`); code == 200 || !strings.Contains(body, `"code":"acknowledgment_required"`) || p.ran("ran") {
		t.Fatalf("without the acknowledgment: %d %s", code, body)
	}
	code, body := p.call(t, apiKey, "POST", "/api/pipelines/ship/run", `{"acknowledged":true,"plan":true}`)
	var shown struct {
		Plan gate.ConnectorPlan `json:"plan"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &shown) != nil || p.ran("ran") {
		t.Fatalf("plan: %d %s", code, body)
	}
	pl := shown.Plan.Plan
	if pl.Lane != "pipeline" || len(pl.Steps) != 2 || pl.Steps[0].Command != "touch ran" || pl.Steps[0].Dir != p.dir || pl.Steps[1].Command != "stop svc" ||
		pl.Digests["pipeline"] == "" || pl.Digests["resource:svc"] == "" || len(pl.Actions) != 1 || pl.Actions[0].Operation != "stop" || pl.Actions[0].Target.Resource != "svc" {
		t.Fatalf("plan %s", body)
	}
	outcomes := p.w.Records(audit.KindOutcome)
	if len(outcomes) == 0 || !outcomes[len(outcomes)-1].DryRun {
		t.Fatalf("the plan is not recorded as a dry run: %+v", outcomes)
	}

	code, body = p.call(t, apiKey, "POST", "/api/pipelines/ship/run", `{"acknowledged":true}`)
	var res supervise.PipelineRunResult
	if code != 200 || json.Unmarshal([]byte(body), &res) != nil || !res.Success || !p.ran("ran") || !strings.Contains(string(res.Raw), `"one"`) {
		t.Fatalf("run: %d %s (raw %s)", code, body, res.Raw)
	}
	var runs int
	for _, r := range p.w.Records(audit.KindIntent) {
		if r.Connector == "pipeline" && r.Operation == "run" {
			runs++
		}
	}
	if runs != 3 {
		t.Fatalf("pipeline.run recorded %d times, want 3", runs)
	}
}

// An approved run uses its approval; an edited stage or an edited resource the pipeline names
// is plan_stale, and nothing runs (main's TestPipelineRunUsesItsApproval).
func TestPipelineRunUsesItsApproval(t *testing.T) {
	for name, change := range map[string]func(dir string, r config.ResourceDef) ([]config.ResourceDef, config.PipelineDef){
		"edited stage": func(dir string, r config.ResourceDef) ([]config.ResourceDef, config.PipelineDef) {
			pd := shellPipeline(dir, "ran")
			pd.Stages[0].Actions[0].Command = "touch other"
			return []config.ResourceDef{r}, pd
		},
		"edited resource": func(dir string, r config.ResourceDef) ([]config.ResourceDef, config.PipelineDef) {
			r.Config = map[string]any{"mode": "dev_session", "command": []any{"/bin/sleep", "61"}}
			return []config.ResourceDef{r}, shellPipeline(dir, "ran")
		},
		"unchanged": nil,
	} {
		t.Run(name, func(t *testing.T) {
			p := newPipelineWorld(t, gatetest.Options{Policy: constantPDP{decision: policy.Approve}, Enforcement: enforceAll{}}, settings.ModeSupervise)
			res := svcResource()
			p.set([]config.ResourceDef{res}, shellPipeline(p.dir, "ran"))
			id := p.pendingID(t, apiKey, "POST", "/api/pipelines/ship/run", `{"acknowledged":true}`)
			if code, body := p.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true,"reason":"go"}`); code != 200 {
				t.Fatalf("approve: %d %s", code, body)
			}
			if change != nil {
				p.set(change(p.dir, res))
				if code, body := p.call(t, apiKey, "POST", "/api/pipelines/ship/run", `{"acknowledged":true,"approval_id":"`+id+`"}`); !strings.Contains(body, `"code":"plan_stale"`) || p.ran("ran") || p.ran("other") {
					t.Fatalf("a stale approval: %d %s", code, body)
				}
				return
			}
			if code, body := p.call(t, apiKey, "POST", "/api/pipelines/ship/run", `{"acknowledged":true,"approval_id":"`+id+`"}`); code != 200 || !strings.Contains(body, `"success":true`) || !p.ran("ran") {
				t.Fatalf("the approved run: %d %s", code, body)
			}
			if got, _ := p.w.Broker.Get(id); got.Status != approval.Consumed {
				t.Fatalf("status %s", got.Status)
			}
		})
	}
}

// A pipeline does not run while a resource it touches is frozen (main's
// TestFreezePausesPipelines); the refusal is recorded.
func TestFreezePausesPipelines(t *testing.T) {
	p := newPipelineWorld(t, gatetest.Options{}, settings.ModeSupervise)
	p.set([]config.ResourceDef{svcResource()}, shellPipeline(p.dir, "ran"))
	p.w.Engage(t, brake.State{Freezes: []brake.Freeze{{ID: "frz-1", Match: policy.TargetMatch{ID: "svc"}, Reason: "release week"}}})
	code, body := p.call(t, apiKey, "POST", "/api/pipelines/ship/run", `{"acknowledged":true}`)
	if code != 423 || !strings.Contains(body, `"code":"frozen"`) || !strings.Contains(body, "touches svc, which is FROZEN by frz-1: release week") || p.ran("ran") {
		t.Fatalf("frozen: %d %s", code, body)
	}
	outcomes := p.w.Records(audit.KindOutcome)
	if len(outcomes) == 0 || outcomes[len(outcomes)-1].OutcomeCode != "frozen" {
		t.Fatalf("outcomes %+v", outcomes)
	}
}

// An agent's run of a pipeline that stops a production resource is an agent's change to
// production: the built-in protections hold it for approval though the pipeline itself is
// unlabeled (main's TestAnAgentsPipelineRunOnProdIsCoveredByTheBuiltInProtections).
func TestAnAgentsPipelineRunOnProdIsCoveredByTheBuiltInProtections(t *testing.T) {
	p := newPipelineWorld(t, gatetest.Options{}, settings.ModeSupervise)
	p.set([]config.ResourceDef{svcResource(func(r *config.ResourceDef) { r.Env = target.EnvProd })}, shellPipeline(p.dir, "ran"))
	p.pendingID(t, apiKey, "POST", "/api/pipelines/ship/run", `{"acknowledged":true}`)
	if p.ran("ran") {
		t.Fatal("the held run ran")
	}
	intents := p.w.Records(audit.KindIntent)
	if len(intents) == 0 || intents[len(intents)-1].Target.Env != string(target.EnvProd) {
		t.Fatalf("the run's target does not carry prod: %+v", intents)
	}
}

// In observe mode a run is refused 409 observe_mode and the list still reads; with no runner
// the routes answer 503.
func TestPipelinesInObserveMode(t *testing.T) {
	p := newPipelineWorld(t, gatetest.Options{}, settings.ModeObserve)
	p.set([]config.ResourceDef{svcResource()}, shellPipeline(p.dir, "ran"))
	if code, body := p.call(t, apiKey, "POST", "/api/pipelines/ship/run", `{"acknowledged":true}`); code != 409 || !strings.Contains(body, `"code":"observe_mode"`) || p.ran("ran") {
		t.Fatalf("observe run: %d %s", code, body)
	}
	if code, body := p.call(t, apiKey, "GET", "/api/pipelines", ""); code != 200 || !strings.Contains(body, `"id":"ship"`) {
		t.Fatalf("observe list: %d %s", code, body)
	}
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	g := &gatedAPI{h: a.Mux()}
	if code, body := g.call(t, apiKey, "GET", "/api/pipelines", ""); code != 503 || !strings.Contains(body, "pipelines_unavailable") {
		t.Fatalf("no runner: %d %s", code, body)
	}
}
