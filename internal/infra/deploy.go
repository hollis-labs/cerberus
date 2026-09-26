package infra

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hollis-labs/cerberus/internal/gitenv"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

type DeploymentRunResult struct {
	Success       bool             `json:"success"`
	ProfileID     string           `json:"profile_id"`
	Provider      string           `json:"provider"`
	DeploymentURL string           `json:"deployment_url,omitempty"`
	Git           GitStatus        `json:"git"`
	Steps         []DeploymentStep `json:"steps"`
	Error         string           `json:"error,omitempty"`
}

type DeploymentStep struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

type GitStatus struct {
	Branch    string   `json:"branch,omitempty"`
	Commit    string   `json:"commit,omitempty"`
	Dirty     bool     `json:"dirty"`
	Remotes   []string `json:"remotes,omitempty"`
	RemoteURL string   `json:"remote_url,omitempty"`
}

// PlannedStep is one command a deployment run executes, as the operator is
// shown it before confirming. A secret never appears in it — only its name,
// in brackets — so the plan is safe to display and to record.
type PlannedStep struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	// Env names the variables the step is given, never their values.
	Env []string `json:"env,omitempty"`
}

// DeploymentPlan is what running a profile will do: the steps, in order, and
// the directory they run in. The run executes exactly these steps.
type DeploymentPlan struct {
	ProfileID string        `json:"profile_id"`
	Provider  string        `json:"provider"`
	RepoPath  string        `json:"repo_path"`
	Steps     []PlannedStep `json:"steps"`
	Error     string        `json:"error,omitempty"`

	steps []deployStep
}

// deployStep is a planned step with what actually runs: a shell command, or
// an argv, plus environment added to the child's. A credential travels only
// in env: argv and a shell string are visible to every local user through
// ps, and a login shell's profile tracing would echo it.
type deployStep struct {
	display string
	name    string
	shell   string
	argv    []string
	env     []string
	after   func(result *DeploymentRunResult, output string)
}

// vercelTokenDisplay is how a step shows the token it is handed. The
// placeholder is a name, never a value, and it must survive redact.Text:
// "--token [vercel token]" did not, because the flag rule read "[vercel" as
// the token and the plan came back as "[REDACTED] token]".
const vercelTokenDisplay = "VERCEL_TOKEN=<vercel token> " //nolint:gosec // the name shown in place of the token, never a value

// PlanDeployment computes the steps RunDeployment would execute for profile,
// without running any of them. It reads credentials only to know whether a
// flag will be passed; their values stay out of the plan.
func PlanDeployment(ctx context.Context, secrets secret.Provider, profile DeploymentProfile) *DeploymentPlan {
	plan := &DeploymentPlan{ProfileID: profile.ID, Provider: profile.Provider, RepoPath: profile.RepoPath}
	if profile.RepoPath == "" {
		plan.Error = "repo_path is required"
		return plan
	}
	if info, err := os.Stat(profile.RepoPath); err != nil || !info.IsDir() {
		plan.Error = fmt.Sprintf("repo path %q is not available", profile.RepoPath)
		return plan
	}
	switch profile.Provider {
	case "vercel":
		plan.steps, plan.Error = planVercel(ctx, secrets, profile)
	default:
		plan.Error = fmt.Sprintf("unsupported deployment provider %q", profile.Provider)
	}
	for _, step := range plan.steps {
		plan.Steps = append(plan.Steps, PlannedStep{Name: step.name, Command: step.display, Env: envNames(step.env)})
	}
	return plan
}

// envNames is the names of NAME=value entries.
func envNames(env []string) []string {
	var names []string
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, name)
	}
	return names
}

func planVercel(ctx context.Context, secrets secret.Provider, profile DeploymentProfile) ([]deployStep, string) {
	token, err := secretValue(ctx, secrets, "vercel", "token")
	if err != nil {
		return nil, secretLookupFailure("vercel/token", err)
	}
	var tokenEnv []string
	if token != "" {
		tokenEnv = []string{"VERCEL_TOKEN=" + token}
	}
	scope := profile.VercelScope
	if scope == "" {
		if scope, err = secretValue(ctx, secrets, "vercel", "scope"); err != nil {
			return nil, secretLookupFailure("vercel/scope", err)
		}
	}
	var steps []deployStep
	if profile.PreflightCommand != "" {
		steps = append(steps, deployStep{name: "preflight", display: profile.PreflightCommand, shell: profile.PreflightCommand})
	}
	if profile.BuildCommand != "" {
		steps = append(steps, deployStep{name: "build", display: profile.BuildCommand, shell: profile.BuildCommand})
	}
	if !hasVercelLink(profile.RepoPath) {
		if profile.VercelProject == "" {
			return steps, "vercel_project is required before first deploy when the repo is not linked"
		}
		argv := []string{"vercel", "link", "--yes", "--project", profile.VercelProject}
		if scope != "" {
			argv = append(argv, "--scope", scope)
		}
		display := strings.Join(argv, " ")
		if token != "" {
			display = vercelTokenDisplay + display
		}
		steps = append(steps, deployStep{name: "link", display: display, argv: argv, env: tokenEnv})
	}

	deployCommand := strings.TrimSpace(profile.DeployCommand)
	if deployCommand == "" {
		deployCommand = "vercel --prod --yes"
	}
	display, shell := deployCommand, deployCommand
	if scope != "" && !strings.Contains(deployCommand, "--scope") {
		display += " --scope " + shellQuote(scope)
		shell += " --scope " + shellQuote(scope)
	}
	if token != "" {
		display = vercelTokenDisplay + display
	}
	steps = append(steps, deployStep{name: "deploy", display: display, shell: shell, env: tokenEnv, after: func(result *DeploymentRunResult, output string) {
		result.DeploymentURL = extractDeploymentURL(output)
	}})
	return steps, ""
}

// RunDeployment executes profile's plan. Each step's recorded command is the
// displayed one, and a credential reaches the child only through its
// environment.
func RunDeployment(ctx context.Context, secrets secret.Provider, profile DeploymentProfile) (*DeploymentRunResult, error) {
	return RunPlannedDeployment(ctx, PlanDeployment(ctx, secrets, profile), profile)
}

// RunPlannedDeployment runs a plan PlanDeployment made, exactly as planned:
// a caller that checked the plan against an approval runs the steps it
// checked rather than planning again.
func RunPlannedDeployment(ctx context.Context, plan *DeploymentPlan, profile DeploymentProfile) (*DeploymentRunResult, error) {
	result := &DeploymentRunResult{ProfileID: profile.ID, Provider: profile.Provider}
	if plan.Error != "" && len(plan.steps) == 0 {
		result.Error = plan.Error
		return result, nil
	}
	result.Git = inspectGitRepo(ctx, profile)
	for _, step := range plan.steps {
		var cmd *exec.Cmd
		if step.shell != "" {
			cmd = exec.CommandContext(ctx, "/bin/sh", "-lc", step.shell) //nolint:gosec // the operator's own profile, confirmed against its plan
		} else {
			cmd = exec.CommandContext(ctx, step.argv[0], step.argv[1:]...) //nolint:gosec // as above
		}
		cmd.Dir = profile.RepoPath
		if len(step.env) > 0 {
			cmd.Env = append(os.Environ(), step.env...)
		}
		var after func(string)
		if step.after != nil {
			after = func(output string) { step.after(result, output) }
		}
		if !executeStep(result, step.name, step.display, cmd, after) {
			break
		}
	}
	if result.Error == "" {
		result.Error = plan.Error
	}
	result.Success = result.Error == ""
	return result, nil
}

func inspectGitRepo(ctx context.Context, profile DeploymentProfile) GitStatus {
	status := GitStatus{}
	status.Branch = strings.TrimSpace(runGitOutput(ctx, profile.RepoPath, "rev-parse", "--abbrev-ref", "HEAD"))
	status.Commit = strings.TrimSpace(runGitOutput(ctx, profile.RepoPath, "rev-parse", "HEAD"))
	status.Dirty = strings.TrimSpace(runGitOutput(ctx, profile.RepoPath, "status", "--porcelain")) != ""
	if remote := strings.TrimSpace(profile.GitRemote); remote != "" {
		status.RemoteURL = strings.TrimSpace(runGitOutput(ctx, profile.RepoPath, "remote", "get-url", remote))
	}
	remotes := strings.TrimSpace(runGitOutput(ctx, profile.RepoPath, "remote"))
	if remotes != "" {
		status.Remotes = strings.Split(remotes, "\n")
	}
	return status
}

func runGitOutput(ctx context.Context, cwd string, args ...string) string {
	out, err := gitenv.Command(ctx, cwd, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func hasVercelLink(repoPath string) bool {
	_, err := os.Stat(filepath.Join(repoPath, ".vercel", "project.json"))
	return err == nil
}

func executeStep(result *DeploymentRunResult, name, command string, cmd *exec.Cmd, after func(string)) bool {
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	step := DeploymentStep{Name: name, Command: command}
	if err := cmd.Run(); err != nil {
		step.Success = false
		step.Output = combined.String()
		step.Error = err.Error()
		result.Steps = append(result.Steps, step)
		result.Error = fmt.Sprintf("%s failed: %v", name, err)
		return false
	}
	step.Success = true
	step.Output = combined.String()
	result.Steps = append(result.Steps, step)
	if after != nil {
		after(step.Output)
	}
	return true
}

// secretValue resolves service/key. A secret that is not stored is "" and no
// error; an error is a lookup that failed — a keychain that refused, a
// reference that did not resolve — and the plan stops on it rather than
// deploying without the credential and failing later for a reason nobody
// sees.
func secretValue(ctx context.Context, provider secret.Provider, service, key string) (string, error) {
	if provider == nil {
		return "", nil
	}
	value, err := provider.Get(ctx, service, key)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

// secretLookupFailure is the plan error for a credential that could not be
// resolved. The name is never followed by a colon, so the assignment rule in
// redact.Text cannot read the message after it as the credential's value.
func secretLookupFailure(name string, err error) string {
	return fmt.Sprintf("could not resolve %s (%v); fix its keychain entry or its reference in connector-secrets.yaml, then plan again", name, err)
}

func lastNonEmptyLine(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

var (
	ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	urlPattern        = regexp.MustCompile(`https?://[^\s]+`)
)

func extractDeploymentURL(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := normalizeOutputLine(lines[i])
		if line == "" || !strings.Contains(strings.ToLower(line), "production") {
			continue
		}
		if candidate := preferredURLFromLine(line, true); candidate != "" {
			return candidate
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := normalizeOutputLine(lines[i])
		if line == "" {
			continue
		}
		if candidate := preferredURLFromLine(line, false); candidate != "" {
			return candidate
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := normalizeOutputLine(lines[i])
		if line == "" {
			continue
		}
		if candidate := preferredURLFromLine(line, true); candidate != "" {
			return candidate
		}
	}
	return ""
}

func preferredURLFromLine(line string, allowVercelDashboard bool) string {
	matches := urlPattern.FindAllString(line, -1)
	for _, match := range matches {
		if deploymentURLCandidate(match, allowVercelDashboard) {
			return match
		}
	}
	return ""
}

func deploymentURLCandidate(raw string, allowVercelDashboard bool) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "vercel.com" || strings.HasSuffix(host, ".vercel.com") {
		return allowVercelDashboard
	}
	return true
}

func normalizeOutputLine(line string) string {
	line = ansiEscapePattern.ReplaceAllString(line, "")
	return strings.TrimSpace(line)
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
