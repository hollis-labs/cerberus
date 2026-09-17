package actions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
)

type fakeLocalApplier struct {
	calls int
	resID string
}

func (f *fakeLocalApplier) Apply(_ context.Context, res *domain.Resource) (localconn.ApplyResult, error) {
	f.calls++
	f.resID = res.ID
	return localconn.ApplyResult{Action: localconn.ApplyActionStarted}, nil
}

func TestDeployActionBuildsInstallsAppliesAndStoresOutputs(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil { //nolint:gosec
		t.Fatalf("mkdir bin: %v", err)
	}
	fakeGo := filepath.Join(binDir, "go")
	if err := os.WriteFile(fakeGo, []byte(`#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    out="$1"
  fi
  shift || true
done
mkdir -p "$(dirname "$out")"
printf 'artifact\n' > "$out"
printf 'build output\n'
`), 0755); err != nil { //nolint:gosec
		t.Fatalf("write fake go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "Makefile"), []byte("install:\n\t@echo install output\n"), 0644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	spec := localconn.ProcessSpec{
		Dir: tmp,
		Env: map[string]string{
			"PATH": binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		},
		BuildStrategy: &localconn.BuildStrategyConfig{
			Kind: "go_standard",
			Rules: map[string]any{
				"target": "./cmd/app",
				"output": filepath.Join(tmp, "app"),
			},
		},
		InstallAfterBuild: true,
	}
	res := &domain.Resource{ID: "app", Config: spec.ToResourceConfig()}
	local := &fakeLocalApplier{}
	action := NewDeploy("app", res, spec, local)
	env := &domain.PipelineEnv{}

	if err := action.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if local.calls != 1 || local.resID != "app" {
		t.Fatalf("Apply calls = %d for %q, want one call for app", local.calls, local.resID)
	}
	if got, ok := env.Values["deploy.app.apply"].(localconn.ApplyResult); !ok || got.Action != localconn.ApplyActionStarted {
		t.Fatalf("deploy.app.apply = %#v, want started ApplyResult", env.Values["deploy.app.apply"])
	}
	if got := env.Values["deploy.app.build_output"]; got != "build output\n" {
		t.Fatalf("deploy.app.build_output = %#v", got)
	}
	if got := env.Values["deploy.app.install_output"]; got != "install output" {
		t.Fatalf("deploy.app.install_output = %#v", got)
	}
	if got := env.Values["deploy.app.install_skipped"]; got != false {
		t.Fatalf("deploy.app.install_skipped = %#v", got)
	}
}
