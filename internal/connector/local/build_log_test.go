package local

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/domain"
)

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
