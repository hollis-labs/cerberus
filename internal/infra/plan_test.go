package infra

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
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
		{"deploy", vercelTokenDisplay + "vercel --prod --yes"},
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

// The token travels only in the child's environment: argv and a shell string
// are readable by every local user through ps while the step runs.
func TestPlanDeploymentKeepsTheTokenOffCommandLines(t *testing.T) {
	const token = "tok-SENTINEL-4f2a" //nolint:gosec // a test sentinel, not a credential
	profile := DeploymentProfile{ID: "site", Provider: "vercel", RepoPath: t.TempDir(),
		VercelProject: "site", VercelScope: "team", DeployCommand: "vercel --prod --yes"}
	plan := PlanDeployment(context.Background(), tokenSecrets{token}, profile)
	if plan.Error != "" {
		t.Fatalf("plan error: %s", plan.Error)
	}
	if len(plan.steps) != 2 || plan.steps[0].name != "link" || plan.steps[1].name != "deploy" {
		t.Fatalf("steps = %+v, want link then deploy", plan.steps)
	}
	for _, step := range plan.steps {
		if strings.Contains(step.shell, token) || strings.Contains(strings.Join(step.argv, " "), token) {
			t.Errorf("%s: token on the command line: shell=%q argv=%q", step.name, step.shell, step.argv)
		}
		if strings.Contains(step.display, token) {
			t.Errorf("%s: token in the displayed command: %q", step.name, step.display)
		}
		if len(step.env) != 1 || step.env[0] != "VERCEL_TOKEN="+token {
			t.Errorf("%s: env = %q, want the token as VERCEL_TOKEN", step.name, step.env)
		}
	}
}

// Every plan response goes out through redact.Marshal, so the displayed
// command has to survive redact.Text intact or the operator confirms against
// "[REDACTED] token]".
func TestPlannedCommandSurvivesRedaction(t *testing.T) {
	profile := DeploymentProfile{ID: "site", Provider: "vercel", RepoPath: t.TempDir(),
		VercelProject: "site", DeployCommand: "vercel --prod --yes"}
	plan := PlanDeployment(context.Background(), tokenSecrets{"tok-SENTINEL-4f2a"}, profile)
	for _, step := range plan.Steps {
		if got := redact.Text(step.Command); got != step.Command {
			t.Errorf("%s: redact.Text(%q) = %q", step.Name, step.Command, got)
		}
	}
}

// Without a token nothing is added to the child's environment or the display.
func TestPlanDeploymentWithoutTokenAddsNoEnv(t *testing.T) {
	profile := DeploymentProfile{ID: "site", Provider: "vercel", RepoPath: linkedRepo(t), DeployCommand: "vercel --prod --yes"}
	plan := PlanDeployment(context.Background(), tokenSecrets{}, profile)
	if len(plan.steps) != 1 || plan.steps[0].env != nil || plan.Steps[0].Command != "vercel --prod --yes" {
		t.Fatalf("steps = %+v, display %+v", plan.steps, plan.Steps)
	}
}

// The run executes the plan with the real credential in the child's
// environment, and records the displayed command, so the token reaches the
// process and never the result.
func TestRunDeploymentRecordsTheDisplayedCommand(t *testing.T) {
	const token = "tok-SENTINEL-4f2a" //nolint:gosec // a test sentinel, not a credential
	const deploy = `printf '%s' "$VERCEL_TOKEN"`
	profile := DeploymentProfile{ID: "site", Provider: "vercel", RepoPath: linkedRepo(t), DeployCommand: deploy}
	result, err := RunDeployment(context.Background(), tokenSecrets{token}, profile)
	if err != nil || !result.Success {
		t.Fatalf("run: %+v %v", result, err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %+v", result.Steps)
	}
	step := result.Steps[0]
	if step.Command != vercelTokenDisplay+deploy {
		t.Fatalf("recorded command %q, want the displayed form", step.Command)
	}
	if step.Output != token {
		t.Fatalf("the process did not receive the real token in VERCEL_TOKEN: %q", step.Output)
	}
}
