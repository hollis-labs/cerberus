package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/domain"
)

func TestBuildProcessResultContextNilOnUnknownKind(t *testing.T) {
	// An unknown build_strategy kind returns (nil, err) — the contract the
	// DeployResource nil-guard relies on, so this must stay true.
	spec := ProcessSpec{BuildStrategy: &BuildStrategyConfig{Kind: "does-not-exist"}}
	res, err := BuildProcessResultContext(context.Background(), spec)
	if err == nil {
		t.Fatal("expected error for unknown build_strategy kind")
	}
	if res != nil {
		t.Fatalf("expected nil result for unknown kind, got %+v", res)
	}
}

func TestWriteBuildLogNilResultDoesNotPanic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	res := &domain.Resource{ID: "x", ProjectID: "p"}
	path, err := WriteBuildLog(res, ProcessSpec{}, nil, fmt.Errorf("boom"))
	if err != nil {
		t.Fatalf("WriteBuildLog(nil result): %v", err)
	}
	data, _ := os.ReadFile(path) //nolint:gosec // test-controlled path
	if !strings.Contains(string(data), "FAILED — boom") {
		t.Fatalf("expected failure marker for nil result, got:\n%s", data)
	}
}

func TestWriteBuildLogCapturesCommandAndOutcome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	res := &domain.Resource{ID: "demo-svc", ProjectID: "demo"}
	spec := ProcessSpec{Dir: "/tmp/demo"}
	result := &BuildResult{
		Output:  "go build -o demo ./cmd/demo\nok\n",
		Command: []string{"make", "build"},
		Dir:     "/tmp/demo",
	}

	// Success capture.
	path, err := WriteBuildLog(res, spec, result, nil)
	if err != nil {
		t.Fatalf("WriteBuildLog: %v", err)
	}
	if want := filepath.Join("demo", "demo-svc", "logs", "build.log"); !strings.HasSuffix(path, want) {
		t.Fatalf("path = %q, want suffix %q", path, want)
	}
	data, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("read build.log: %v", err)
	}
	got := string(data)
	for _, want := range []string{"command: make build", "dir: /tmp/demo", "result: ok", "go build -o demo"} {
		if !strings.Contains(got, want) {
			t.Fatalf("build.log missing %q in:\n%s", want, got)
		}
	}

	// Failure capture replaces the previous log and records the error.
	if _, err := WriteBuildLog(res, spec, result, fmt.Errorf("exit status 2")); err != nil {
		t.Fatalf("WriteBuildLog (fail): %v", err)
	}
	data, _ = os.ReadFile(path) //nolint:gosec // test-controlled path
	if !strings.Contains(string(data), "FAILED — exit status 2") {
		t.Fatalf("expected failure marker, got:\n%s", data)
	}
}

func TestBuildCommandSummary(t *testing.T) {
	spec := ProcessSpec{Dir: "/x", BuildStrategy: &BuildStrategyConfig{Kind: "make_standard"}}
	if got := BuildCommandSummary(spec, &BuildResult{Command: []string{"make", "build"}, Dir: "/x"}); got != "`make build` in /x" {
		t.Fatalf("summary = %q", got)
	}
	if got := BuildCommandSummary(spec, nil); got != `build_strategy "make_standard" in /x` {
		t.Fatalf("fallback summary = %q", got)
	}
}

func TestBuildAndInstallRedactKnownEnvironmentValues(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	writeMakefile(t, dir, "build:\n\t@echo $$API_KEY\ninstall:\n\t@echo $$API_KEY\n")
	spec := ProcessSpec{Dir: dir, Env: map[string]string{"API_KEY": "opaque-build-sentinel"}, BuildStrategy: &BuildStrategyConfig{Kind: "make_standard"}}
	result, err := BuildProcessResultContext(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Output, "opaque-build-sentinel") || !strings.Contains(result.Output, "[REDACTED]") {
		t.Fatal("build output leaked")
	}
	_, output, err := RunInstall(spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "opaque-build-sentinel") || !strings.Contains(output, "[REDACTED]") {
		t.Fatal("install output leaked")
	}
}
