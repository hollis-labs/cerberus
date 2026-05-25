package actions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
)

func TestBuildActionStoresArtifactsInPipelineEnv(t *testing.T) {
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
`), 0755); err != nil { //nolint:gosec
		t.Fatalf("write fake go: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	action := NewBuild("app", localconn.ProcessSpec{
		Dir: tmp,
		Env: map[string]string{
			"PATH":    binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
			"VERSION": "v1.0.0",
		},
		BuildStrategy: &localconn.BuildStrategyConfig{
			Kind: "go_standard",
			Rules: map[string]any{
				"target": "./cmd/app",
				"matrix": map[string]any{
					"os":   []any{"darwin"},
					"arch": []any{"arm64"},
				},
				"artifacts": map[string]any{
					"name":     "app-${VERSION}-${os}-${arch}",
					"archive":  true,
					"checksum": true,
				},
			},
		},
	})
	env := &domain.PipelineEnv{}
	if err := action.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	raw := env.Values["build.app.artifacts"]
	artifacts, ok := raw.([]localconn.BuildArtifact)
	if !ok {
		t.Fatalf("build.app.artifacts = %T, want []BuildArtifact", raw)
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts len = %d, want 1", len(artifacts))
	}
	if artifacts[0].Checksum == "" {
		t.Fatalf("artifact checksum is empty")
	}
	if env.Values["build.latest.artifacts"] == nil {
		t.Fatalf("build.latest.artifacts missing")
	}
}
