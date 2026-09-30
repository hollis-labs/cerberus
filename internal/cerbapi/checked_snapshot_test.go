package cerbapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
)

// shiftingLookup is a config edited while a call is in flight: the first
// resolve of dev-box says one host and dev, every later one another host
// and prod.
func shiftingLookup(calls *int) func(string) (*config.ResourceDef, bool) {
	return shifting(calls, true)
}

// shifting is shiftingLookup, with relabel false changing only the host.
func shifting(calls *int, relabel bool) func(string) (*config.ResourceDef, bool) {
	return func(id string) (*config.ResourceDef, bool) {
		if id != "dev-box" {
			return nil, false
		}
		*calls++
		def := config.ResourceDef{ID: "dev-box", Type: "container", Connector: "docker", Env: target.EnvDev, Owner: target.OwnerSelf,
			Admin: target.Admin{Default: target.AdminSelf}, Config: map[string]any{"host": "ssh://checked"}}
		if *calls > 1 {
			def.Config = map[string]any{"host": "ssh://edited"}
			if relabel {
				def.Env = target.EnvProd
			}
		}
		return &def, true
	}
}

// An admin-lane call resolves its configured resource once (M10): the
// gate's labels, the plan and the run all use that resolve, so an edit to
// the resource between them does not run.
func TestAnAdminCallRunsTheResourceItWasCheckedAgainst(t *testing.T) {
	sink := audit.NewMemory()
	svc, backend := dockerLane(t, sink)
	var calls int
	svc.SetResourceLookup(shiftingLookup(&calls))
	if _, err := svc.Execute(BeginRequest(context.Background(), SurfaceInProcess), devStop()); err != nil {
		t.Fatal(err)
	}
	if backend.stopped != "web" || backend.target.Host != "ssh://checked" {
		t.Fatalf("ran against %+v", backend.target)
	}
	if calls != 1 {
		t.Fatalf("the resource was resolved %d times", calls)
	}
	if intent := sink.Records()[0]; intent.Target.Env != string(target.EnvDev) {
		t.Fatalf("labeled %+v", intent.Target)
	}
}

// The admin plan binds the target as it would run: the configured
// resource's merged config, keyed (M10).
func TestAnAdminPlanBindsTheResolvedTarget(t *testing.T) {
	svc, _ := dockerLane(t, audit.NewMemory())
	var calls int
	svc.SetResourceLookup(shifting(&calls, false))
	ctx := BeginRequest(context.Background(), SurfaceInProcess)
	first := shownHash(ctx, t, svc, devStop())
	second := shownHash(ctx, t, svc, devStop())
	if first == second {
		t.Fatal("a plan did not change when its resource's host did")
	}
}

// A resource verb resolves the config once (M10): the definition its gate
// labeled and planned is the one it runs, whatever the config file says
// by the time it runs.
func TestAResourceVerbRunsTheConfigItWasCheckedAgainst(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	write := func(dir string) {
		t.Helper()
		body := "version: 2\nresources:\n  - id: web\n    type: process\n    connector: local\n    env: dev\n    owner: self\n    config:\n      mode: dev_session\n      command: [\"/bin/sleep\", \"60\"]\n      dir: " + dir + "\n"
		if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil { //nolint:gosec // the test's own temp dir
			t.Fatal(err)
		}
	}
	checked, edited := filepath.Join(dir, "checked"), filepath.Join(dir, "edited")
	write(checked)
	svc := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigPath(cfgPath))
	ctx := withConfigSnapshot(context.Background(), svc.snapshotConfig())
	write(edited)
	res, err := svc.requireLocalProcessResource(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Config["dir"]; got != checked {
		t.Fatalf("ran the edited definition: dir %v", got)
	}
	if fresh, _ := svc.requireLocalProcessResource(context.Background(), "web"); fresh.Config["dir"] != edited {
		t.Fatal("outside a gated call the config is read fresh")
	}
}

// editingPDP allows everything, and edits the config file as it decides:
// a config changed after the gate read it and before the run.
type editingPDP struct {
	constantPDP
	edit func()
}

func (e editingPDP) Authorize(r policy.Request) policy.Result {
	e.edit()
	return e.constantPDP.Authorize(r)
}

// End to end through a gated resource verb: the resource is removed from
// the config while the gate runs, and the verb still runs the definition
// the gate checked, instead of the config as it reads by then (M10).
func TestAGatedVerbRunsThroughAnEditAfterTheGate(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	body := "version: 2\nresources:\n  - id: web\n    type: process\n    connector: local\n    env: dev\n    owner: self\n    config:\n      mode: dev_session\n      command: [\"/bin/sleep\", \"60\"]\n      dir: " + dir + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	withPDP(t, editingPDP{constantPDP{decision: policy.Allow}, func() {
		_ = os.WriteFile(cfgPath, []byte(strings.Replace(body, "id: web", "id: renamed", 1)), 0o600)
	}})
	svc := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigPath(cfgPath))
	out, err := svc.StopResource(BeginRequest(context.Background(), SurfaceInProcess), "web", WithAcknowledged(true))
	if err != nil || out == nil || !out.Success {
		t.Fatalf("the run re-read the config: %+v %v", out, err)
	}
	out, err = svc.StopResource(BeginRequest(context.Background(), SurfaceInProcess), "web", WithAcknowledged(true))
	if err != nil || out == nil || !strings.Contains(out.Error, "not found") {
		t.Fatalf("the next call reads the edited config: %+v %v", out, err)
	}
}

// A deploy's checkout is read once at the gate (M10): a tree edited after
// the gate decided is refused under the build lock, and nothing builds.
func TestADeployRefusesATreeEditedAfterTheGate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	testGit(t, repo, "init", "-q")
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil { //nolint:gosec // the test's own temp repo
			t.Fatal(err)
		}
	}
	write("Makefile", "build:\n\t@touch built\n")
	testGit(t, repo, "add", ".")
	testGit(t, repo, "commit", "-q", "-m", "one")
	withPDP(t, editingPDP{constantPDP{decision: policy.Allow}, func() { write("late.txt", "after the gate\n") }})
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: "app", Type: "process", Connector: "local", Config: map[string]any{
		"dir": repo, "command": []string{"./app"}, "install_after_build": false,
		"build_strategy": map[string]any{"kind": "make_standard", "rules": map[string]any{"output": "./app"}},
	}}}}
	svc := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(cfg))
	out, err := svc.DeployResource(BeginRequest(context.Background(), SurfaceInProcess), "app", WithAcknowledged(true))
	if err != nil || out == nil || out.Success || !strings.Contains(out.Error, "changed after this deploy was checked") {
		t.Fatalf("deploy: %+v %v", out, err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "built")); statErr == nil {
		t.Fatal("the edited tree was built")
	}
}

// The deploy plan hashes the tree the gate read, so the approval binds the
// tree the build is checked against, not one read later.
func TestADeployPlanHashesTheCheckedTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	testGit(t, repo, "init", "-q")
	testGit(t, repo, "commit", "-q", "--allow-empty", "-m", "one")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one"), 0o600); err != nil { //nolint:gosec // the test's own temp repo
		t.Fatal(err)
	}
	res := &config.ResourceDef{ID: "app", Type: "process", Connector: "local", Config: map[string]any{
		"dir": repo, "command": []string{"./app"}, "build_strategy": map[string]any{"kind": "make_standard", "rules": map[string]any{"output": "./app"}},
	}}
	svc := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(&config.ConfigV2{Resources: []config.ResourceDef{*res}}))
	ctx := withCheckedSource(context.Background(), repo)
	atGate := gitSource(context.Background(), repo)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two"), 0o600); err != nil { //nolint:gosec // as above
		t.Fatal(err)
	}
	p, err := svc.planResourceDef(ctx, auditSpec{connector: "local", operation: "deploy"}, res, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source == nil || p.Source.Content != atGate.Content {
		t.Fatalf("the plan read the tree again: %+v", p.Source)
	}
}
