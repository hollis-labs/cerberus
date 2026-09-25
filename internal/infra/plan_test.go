package infra

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tokenSecrets struct{ token string }

func (s tokenSecrets) Get(_ context.Context, service, key string) (string, error) {
	if service == "vercel" && key == "token" {
		return s.token, nil
	}
	return "", nil
}
func (tokenSecrets) Set(context.Context, string, string, string) error { return nil }
func (tokenSecrets) Delete(context.Context, string, string) error      { return nil }

func linkedRepo(t *testing.T) string {
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

// The plan is what the operator confirms against (Decision 3): every
// command, in order, with a credential named and never shown.
func TestPlanDeploymentShowsCommandsWithoutSecrets(t *testing.T) {
	const token = "tok-SENTINEL-4f2a" //nolint:gosec // a test sentinel, not a credential
	profile := DeploymentProfile{ID: "site", Provider: "vercel", RepoPath: linkedRepo(t),
		PreflightCommand: "pnpm check", BuildCommand: "pnpm build", DeployCommand: "vercel --prod --yes"}
	plan := PlanDeployment(context.Background(), tokenSecrets{token}, profile)
	if plan.Error != "" {
		t.Fatalf("plan error: %s", plan.Error)
	}
	want := []PlannedStep{
		{"preflight", "pnpm check"},
		{"build", "pnpm build"},
		{"deploy", "vercel --prod --yes --token " + vercelTokenPlaceholder},
	}
	if len(plan.Steps) != len(want) {
		t.Fatalf("steps = %+v, want %+v", plan.Steps, want)
	}
	for i, step := range plan.Steps {
		if step != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, step, want[i])
		}
		if strings.Contains(step.Command, token) {
			t.Fatalf("plan shows the token: %q", step.Command)
		}
	}
}

// The run executes the plan with the real credential, and records the
// displayed command — so the token reaches the process and never the result.
func TestRunDeploymentRecordsTheDisplayedCommand(t *testing.T) {
	const token = "tok-SENTINEL-4f2a" //nolint:gosec // a test sentinel, not a credential
	profile := DeploymentProfile{ID: "site", Provider: "vercel", RepoPath: linkedRepo(t), DeployCommand: "echo"}
	result, err := RunDeployment(context.Background(), tokenSecrets{token}, profile)
	if err != nil || !result.Success {
		t.Fatalf("run: %+v %v", result, err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %+v", result.Steps)
	}
	step := result.Steps[0]
	if step.Command != "echo --token "+vercelTokenPlaceholder {
		t.Fatalf("recorded command %q, want the displayed form", step.Command)
	}
	if !strings.Contains(step.Output, token) {
		t.Fatalf("the process did not receive the real token: %q", step.Output)
	}
}
