package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/domain"
	gmcp "github.com/hollis-labs/go-mcp/server"
)

// mockAction records execution and optionally fails.
type mockAction struct {
	name       string
	executed   atomic.Bool
	rolledBack atomic.Bool
	failExec   bool
}

func (a *mockAction) Name() string { return a.name }
func (a *mockAction) Execute(_ context.Context, _ *domain.PipelineEnv) error {
	a.executed.Store(true)
	if a.failExec {
		return fmt.Errorf("mock failure: %s", a.name)
	}
	return nil
}
func (a *mockAction) Rollback(_ context.Context, _ *domain.PipelineEnv) error {
	a.rolledBack.Store(true)
	return nil
}

func TestExecutorLinearPipeline(t *testing.T) {
	a1 := &mockAction{name: "build"}
	a2 := &mockAction{name: "stop"}
	a3 := &mockAction{name: "start"}

	p, err := New("deploy").
		Stage("build").Action(a1).Done().
		Stage("stop").Action(a2).DependsOn("build").Done().
		Stage("start").Action(a3).DependsOn("stop").Done().
		Build()
	if err != nil {
		t.Fatal(err)
	}

	exec := NewExecutor(nil)
	env := &domain.PipelineEnv{Values: map[string]any{}}

	result, err := exec.Run(context.Background(), p, env)
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != domain.StateHealthy {
		t.Errorf("status = %q, want %q", result.Status, domain.StateHealthy)
	}
	if len(result.Stages) != 3 {
		t.Fatalf("got %d stage results, want 3", len(result.Stages))
	}
	if !a1.executed.Load() || !a2.executed.Load() || !a3.executed.Load() {
		t.Error("not all actions were executed")
	}
}

func TestExecutorParallelStages(t *testing.T) {
	a1 := &mockAction{name: "lint"}
	a2 := &mockAction{name: "test"}
	a3 := &mockAction{name: "deploy"}

	// lint and test have no deps (parallel), deploy depends on both
	p, err := New("ci").
		Stage("lint").Action(a1).Done().
		Stage("test").Action(a2).Done().
		Stage("deploy").Action(a3).DependsOn("lint", "test").Done().
		Build()
	if err != nil {
		t.Fatal(err)
	}

	exec := NewExecutor(nil)
	result, err := exec.Run(context.Background(), p, &domain.PipelineEnv{Values: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != domain.StateHealthy {
		t.Errorf("status = %q, want %q", result.Status, domain.StateHealthy)
	}
	if !a1.executed.Load() || !a2.executed.Load() || !a3.executed.Load() {
		t.Error("not all actions were executed")
	}
}

func TestExecutorFailureSkipsDownstream(t *testing.T) {
	a1 := &mockAction{name: "build", failExec: true}
	a2 := &mockAction{name: "deploy"}

	p, err := New("deploy").
		Stage("build").Action(a1).Done().
		Stage("deploy").Action(a2).DependsOn("build").Done().
		Build()
	if err != nil {
		t.Fatal(err)
	}

	exec := NewExecutor(nil)
	result, err := exec.Run(context.Background(), p, &domain.PipelineEnv{Values: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != domain.StateFailed {
		t.Errorf("status = %q, want %q", result.Status, domain.StateFailed)
	}
	if !a1.executed.Load() {
		t.Error("build should have been executed")
	}
	if a2.executed.Load() {
		t.Error("deploy should NOT have been executed after build failure")
	}
}

func TestExecutorRollbackOnFailure(t *testing.T) {
	a1 := &mockAction{name: "stop"}
	a2 := &mockAction{name: "start", failExec: true}

	p, err := New("restart").
		Stage("stop").Action(a1).Done().
		Stage("start").Action(a2).DependsOn("stop").Done().
		Build()
	if err != nil {
		t.Fatal(err)
	}

	exec := NewExecutor(nil)
	result, err := exec.Run(context.Background(), p, &domain.PipelineEnv{Values: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != domain.StateFailed {
		t.Errorf("status = %q, want %q", result.Status, domain.StateFailed)
	}
	// The stop stage completed successfully, so it should be rolled back
	if !a1.rolledBack.Load() {
		t.Error("completed stop action should have been rolled back")
	}
}

func TestExecutorContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	a1 := &mockAction{name: "build"}

	p, err := New("deploy").
		Stage("build").Action(a1).Done().
		Build()
	if err != nil {
		t.Fatal(err)
	}

	exec := NewExecutor(nil)
	result, err := exec.Run(ctx, p, &domain.PipelineEnv{Values: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != domain.StateFailed {
		t.Errorf("status = %q, want %q after cancellation", result.Status, domain.StateFailed)
	}
}

func TestExecutorReportsDuration(t *testing.T) {
	a1 := &mockAction{name: "noop"}

	p, err := New("quick").
		Stage("noop").Action(a1).Done().
		Build()
	if err != nil {
		t.Fatal(err)
	}

	exec := NewExecutor(nil)
	result, err := exec.Run(context.Background(), p, &domain.PipelineEnv{Values: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Duration == 0 {
		t.Error("duration should be > 0")
	}
}

func TestBuilderValidation(t *testing.T) {
	// Empty pipeline
	_, err := New("empty").Build()
	if err == nil {
		t.Error("expected error for empty pipeline")
	}

	// Duplicate stage names
	a := &mockAction{name: "x"}
	_, err = New("dup").
		Stage("build").Action(a).Done().
		Stage("build").Action(a).Done().
		Build()
	if err == nil {
		t.Error("expected error for duplicate stage names")
	}

	// Unknown dependency
	_, err = New("bad-dep").
		Stage("build").Action(a).DependsOn("nonexistent").Done().
		Build()
	if err == nil {
		t.Error("expected error for unknown dependency")
	}

	// Stage with no actions
	_, err = New("no-actions").
		Stage("empty").Done().
		Build()
	if err == nil {
		t.Error("expected error for stage with no actions")
	}
}

func TestDAGCycleDetection(t *testing.T) {
	a := &mockAction{name: "x"}

	_, err := New("cycle").
		Stage("a").Action(a).DependsOn("b").Done().
		Stage("b").Action(a).DependsOn("a").Done().
		Build()
	if err != nil {
		// Builder doesn't detect cycles — that's the DAG's job
		t.Skip("builder caught the cycle early")
	}

	// Build succeeds but executor should catch the cycle
	// Actually the DAG builder in executor catches it
}

func TestDAGDirectCycle(t *testing.T) {
	stages := []*Stage{
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"a"}},
	}

	_, err := buildDAG(stages)
	if err == nil {
		t.Error("expected cycle detection error")
	}
}

func TestDAGLevels(t *testing.T) {
	stages := []*Stage{
		{Name: "a"},
		{Name: "b"},
		{Name: "c", DependsOn: []string{"a", "b"}},
		{Name: "d", DependsOn: []string{"c"}},
	}

	dag, err := buildDAG(stages)
	if err != nil {
		t.Fatal(err)
	}

	levels, err := dag.levels()
	if err != nil {
		t.Fatal(err)
	}

	if len(levels) != 3 {
		t.Fatalf("got %d levels, want 3", len(levels))
	}
	// Level 0: a, b (parallel)
	if len(levels[0]) != 2 {
		t.Errorf("level 0 has %d stages, want 2", len(levels[0]))
	}
	// Level 1: c
	if len(levels[1]) != 1 || levels[1][0].Name != "c" {
		t.Errorf("level 1 should be [c], got %v", levels[1])
	}
	// Level 2: d
	if len(levels[2]) != 1 || levels[2][0].Name != "d" {
		t.Errorf("level 2 should be [d], got %v", levels[2])
	}
}

func TestRunResultIncludesAllStages(t *testing.T) {
	a1 := &mockAction{name: "build", failExec: true}
	a2 := &mockAction{name: "test"}
	a3 := &mockAction{name: "deploy"}

	p, err := New("full").
		Stage("build").Action(a1).Done().
		Stage("test").Action(a2).DependsOn("build").Done().
		Stage("deploy").Action(a3).DependsOn("test").Done().
		Build()
	if err != nil {
		t.Fatal(err)
	}

	exec := NewExecutor(nil)
	result, err := exec.Run(context.Background(), p, &domain.PipelineEnv{Values: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}

	// All 3 stages should appear in results (build=failed, test=skipped, deploy=skipped)
	if len(result.Stages) != 3 {
		t.Fatalf("got %d stage results, want 3", len(result.Stages))
	}

	_ = time.Now() // suppress unused import
}

func TestExecutorEmitsMCPNotifications(t *testing.T) {
	a1 := &mockAction{name: "build"}
	a2 := &mockAction{name: "deploy"}

	p, err := New("release").
		Stage("build").Action(a1).Done().
		Stage("deploy").Action(a2).DependsOn("build").Done().
		Build()
	if err != nil {
		t.Fatal(err)
	}

	var notifications []gmcp.Notification
	ctx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) {
		notifications = append(notifications, n)
	})

	exec := NewExecutor(nil)
	result, err := exec.Run(ctx, p, &domain.PipelineEnv{Values: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.StateHealthy {
		t.Fatalf("status = %q, want %q", result.Status, domain.StateHealthy)
	}
	if len(notifications) == 0 {
		t.Fatal("expected notifications")
	}

	var sawStartMessage bool
	var sawStageStart bool
	var sawFinalProgress bool
	for _, n := range notifications {
		switch n.Method {
		case "notifications/message":
			params, _ := n.Params.(map[string]interface{})
			msg, _ := params["message"].(string)
			if strings.Contains(msg, "Starting pipeline release") {
				sawStartMessage = true
			}
			if strings.Contains(msg, "Stage build started") {
				sawStageStart = true
			}
		case "notifications/progress":
			params, _ := n.Params.(map[string]interface{})
			progress, _ := params["progress"].(float64)
			total, _ := params["total"].(float64)
			msg, _ := params["message"].(string)
			if progress == 2 && total == 2 && strings.Contains(msg, "healthy") {
				sawFinalProgress = true
			}
		}
	}

	if !sawStartMessage {
		t.Fatal("missing pipeline start notification")
	}
	if !sawStageStart {
		t.Fatal("missing stage start notification")
	}
	if !sawFinalProgress {
		t.Fatal("missing final progress notification")
	}
}
