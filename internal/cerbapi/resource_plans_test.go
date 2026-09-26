package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

func devResource(t *testing.T, cfg map[string]any) config.ResourceDef {
	t.Helper()
	dir := t.TempDir()
	base := map[string]any{"dir": dir, "command": []string{"sleep", "60"}, "log_file": filepath.Join(dir, "runtime.log")}
	for k, v := range cfg {
		base[k] = v
	}
	return config.ResourceDef{ID: "svc", Type: "process", Connector: "local", Config: base,
		Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}}
}

func setResources(svc *ResourceRuntimeService, defs ...config.ResourceDef) {
	svc.cfgMu.Lock()
	svc.cfg = &config.ConfigV2{Resources: defs}
	svc.cfgMu.Unlock()
}

// A plan asked for on a resource verb runs nothing, is recorded as a dry
// run, and is the plan an approval of the verb binds to.
func TestResourcePlanOnRequest(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	svc := NewResourceRuntimeService(sink)
	setResources(svc, devResource(t, nil))
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	shown, err := svc.StopResource(ctx, "svc", WithAcknowledged(true), WithPlan())
	if err != nil || shown.Plan == nil {
		t.Fatalf("plan: %+v %v", shown, err)
	}
	p := shown.Plan.Plan
	if p.Lane != plan.LaneResource || p.Target.Resource != "svc" || p.Digests["spec"] == "" || p.State == "" {
		t.Fatalf("plan %+v", p)
	}
	if o := outcome(sink.Records()); !o.DryRun || o.Decision == audit.DecisionRefused {
		t.Fatalf("a plan request was not recorded as an allowed dry run: %+v", o)
	}
	_, err = svc.StopResource(ctx, "svc", WithAcknowledged(true))
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil {
		t.Fatalf("asking: %v", err)
	}
	if a, _ := broker.Get(coded.Approval.ID); a.PlanHash != shown.Plan.PlanHash {
		t.Fatalf("approval binds %s, plan shown %s", a.PlanHash, shown.Plan.PlanHash)
	}
}

// An approved resource verb runs under its id; an edited resource or a
// relabeled one is plan_stale and nothing runs.
func TestResourceVerbUsesItsApproval(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	ask := func(svc *ResourceRuntimeService) approval.Approval {
		t.Helper()
		_, err := svc.StopResource(ctx, "svc", WithAcknowledged(true))
		var coded *ExternalConnectorError
		if !errors.As(err, &coded) || coded.Code != ExternalConnectorApprovalPending {
			t.Fatalf("asking: %v", err)
		}
		a, err := broker.Decide(ctx, coded.Approval.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Via: "cli"}})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	for name, change := range map[string]func(config.ResourceDef) config.ResourceDef{
		"edited spec": func(d config.ResourceDef) config.ResourceDef {
			d.Config["command"] = []string{"sleep", "61"}
			return d
		},
		"relabeled": func(d config.ResourceDef) config.ResourceDef {
			d.Tags = []string{"shared"}
			return d
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := NewResourceRuntimeService(sink)
			def := devResource(t, nil)
			setResources(svc, def)
			a := ask(svc)
			setResources(svc, change(def))
			if _, err := svc.StopResource(ctx, "svc", WithAcknowledged(true), WithApprovalID(a.ID)); connectorErrorCode(err) != ExternalConnectorPlanStale {
				t.Fatalf("err = %v", err)
			}
			if got, _ := broker.Get(a.ID); got.Status != approval.Approved {
				t.Fatalf("status %s", got.Status)
			}
		})
	}
	t.Run("unchanged", func(t *testing.T) {
		svc := NewResourceRuntimeService(sink)
		setResources(svc, devResource(t, nil))
		a := ask(svc)
		if _, err := svc.StopResource(ctx, "svc", WithAcknowledged(true), WithApprovalID(a.ID)); err != nil {
			t.Fatalf("approved stop: %v", err)
		}
		if o := outcome(sink.Records()); o.ApprovalID != a.ID || o.PlanHash != a.PlanHash {
			t.Fatalf("outcome %+v", o)
		}
	})
}

// An os_service resource's apply plan binds the launch agent it would
// write: a changed environment is a different plist, and nothing is
// written to compute it.
func TestApplyPlanBindsThePlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	sink := audit.NewMemory()
	svc := NewResourceRuntimeService(sink)
	def := devResource(t, map[string]any{"mode": "os_service", "supervisor": "launchd", "service_name": "com.example.plan-test", "env": map[string]any{"LEVEL": "info"}})
	setResources(svc, def)
	spec := auditSpec{connector: "local", operation: localconn.OpApply, config: map[string]any{"id": "svc"}}
	first, err := svc.planResource(context.Background(), spec, "svc")
	if err != nil || first.Digests["plist"] == "" {
		t.Fatalf("plan %+v %v", first, err)
	}
	def.Config["env"] = map[string]any{"LEVEL": "debug"}
	setResources(svc, def)
	second, err := svc.planResource(context.Background(), spec, "svc")
	if err != nil || second.Digests["plist"] == first.Digests["plist"] {
		t.Fatalf("a changed environment rendered the same plist: %v", err)
	}
	if strings.Contains(first.Digests["plist"], "info") {
		t.Fatal("the plist's values reached the plan")
	}
	if matches, _ := filepath.Glob(filepath.Join(home, "Library", "LaunchAgents", "*")); len(matches) != 0 {
		t.Fatalf("computing a plan wrote %v", matches)
	}
}

// acceptPresence accepts every decision, for tests.
type acceptPresence struct{}

func (acceptPresence) Verify(approval.Approval, approval.Decision) error { return nil }

// countingSecrets counts lookups: each deployment plan reads the token once.
type countingSecrets struct{ gets *int }

func (c countingSecrets) Get(context.Context, string, string) (string, error) {
	*c.gets++
	return "", nil
}
func (countingSecrets) Set(context.Context, string, string, string) error { return nil }
func (countingSecrets) Delete(context.Context, string, string) error      { return nil }

var _ secret.Provider = countingSecrets{}

// A deploy-profile run under an approval runs the plan the gate checked; it
// does not plan again (CERB-GAP-878).
func TestApprovedDeployRunsTheCheckedPlan(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	// The profile's target is unlabeled, so out of band: a test verifier
	// stands in for P3-4's presence proof.
	broker.SetPresenceVerifier(acceptPresence{})
	repo := linkedVercelRepo(t)
	gets := 0
	secrets := countingSecrets{gets: &gets}
	profile := infra.DeploymentProfile{ID: "site", Provider: "vercel", RepoPath: repo, DeployCommand: "true", VercelScope: "team"}
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	_, err = RunDeploymentProfile(ctx, sink, secrets, profile, WithAcknowledged(true))
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil {
		t.Fatalf("asking: %v", err)
	}
	if _, derr := broker.Decide(ctx, coded.Approval.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human"}}); derr != nil {
		t.Fatal(derr)
	}
	before := gets
	result, err := RunDeploymentProfile(ctx, sink, secrets, profile, WithAcknowledged(true), WithApprovalID(coded.Approval.ID))
	if err != nil || result == nil || !result.Success {
		t.Fatalf("approved run: %+v %v", result, err)
	}
	if planned := gets - before; planned != 1 {
		t.Fatalf("the approved run planned %d times, want once: the check's plan is the one that runs", planned)
	}
}

// linkedVercelRepo is a checkout already linked to a Vercel project, so a
// profile's plan needs no project name.
func linkedVercelRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".vercel"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".vercel", "project.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo
}

// A resource plan over the socket takes the plan route: a current daemon
// answers with the plan and runs nothing, and a daemon that predates plans
// refuses the route rather than running the verb.
func TestResourcePlanRoute(t *testing.T) {
	svc := NewResourceRuntimeService(audit.NewMemory())
	setResources(svc, devResource(t, nil))
	client := startConnectorSocket(t, NewInProcessClient(WithResourceRuntimeService(svc)))
	out, err := client.StopResource(context.Background(), "svc", WithAcknowledged(true), WithPlan())
	if err != nil || out.Plan == nil || !strings.HasPrefix(out.Plan.PlanHash, "sha256:") {
		t.Fatalf("plan over the socket: %+v %v", out, err)
	}
	// deploy is a streamed route; its plan comes back the same way.
	if deployed, derr := client.DeployResource(context.Background(), "svc", WithAcknowledged(true), WithPlan()); derr != nil || deployed.Plan == nil || deployed.BuildPerformed {
		t.Fatalf("deploy plan over the socket: %+v %v", deployed, derr)
	}

	sock := filepath.Join(tempSocketDir(t), "old.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int32
	mux := http.NewServeMux()
	// The pre-plan handler: an action it does not know is a 404.
	mux.HandleFunc("/resources/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/resources/"), "/", 2)
		switch action := parts[1]; action {
		case "deploy", "apply", "reload", "stop", "sync", "remove":
			ran.Add(1)
			writeJSON(w, http.StatusOK, OpResult{Success: true, ServiceID: parts[0]})
		default:
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown resource action %q", action))
		}
	})
	srv := &http.Server{Handler: mux} //nolint:gosec // a test server on a temp unix socket
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	_, err = NewSocketClient(sock).StopResource(context.Background(), "svc", WithAcknowledged(true), WithPlan())
	if err == nil || !strings.Contains(err.Error(), "predates plans") || ran.Load() != 0 {
		t.Fatalf("old daemon: %v, ran %d", err, ran.Load())
	}
}
