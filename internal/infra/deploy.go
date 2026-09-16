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

func RunDeployment(ctx context.Context, secrets secret.Provider, profile DeploymentProfile) (*DeploymentRunResult, error) {
	result := &DeploymentRunResult{
		ProfileID: profile.ID,
		Provider:  profile.Provider,
	}
	if profile.RepoPath == "" {
		result.Error = "repo_path is required"
		return result, nil
	}
	info, err := os.Stat(profile.RepoPath)
	if err != nil || !info.IsDir() {
		result.Error = fmt.Sprintf("repo path %q is not available", profile.RepoPath)
		return result, nil
	}

	result.Git = inspectGitRepo(ctx, profile)

	switch profile.Provider {
	case "vercel":
		runVercelDeployment(ctx, secrets, profile, result)
	default:
		result.Error = fmt.Sprintf("unsupported deployment provider %q", profile.Provider)
	}
	result.Success = result.Error == ""
	return result, nil
}

func runVercelDeployment(ctx context.Context, secrets secret.Provider, profile DeploymentProfile, result *DeploymentRunResult) {
	token := secretValue(ctx, secrets, "vercel", "token")
	scope := profile.VercelScope
	if scope == "" {
		scope = secretValue(ctx, secrets, "vercel", "scope")
	}

	if profile.PreflightCommand != "" {
		if !runShellStep(ctx, profile, result, "preflight", profile.PreflightCommand, nil) {
			return
		}
	}
	if profile.BuildCommand != "" {
		if !runShellStep(ctx, profile, result, "build", profile.BuildCommand, nil) {
			return
		}
	}

	if !hasVercelLink(profile.RepoPath) {
		if profile.VercelProject == "" {
			result.Error = "vercel_project is required before first deploy when the repo is not linked"
			return
		}
		args := []string{"link", "--yes", "--project", profile.VercelProject}
		if scope != "" {
			args = append(args, "--scope", scope)
		}
		if token != "" {
			args = append(args, "--token", token)
		}
		if !runCommandStep(ctx, profile, result, "link", "vercel", args...) {
			return
		}
	}

	deployCommand := profile.DeployCommand
	if strings.TrimSpace(deployCommand) == "" {
		deployCommand = "vercel --prod --yes"
	}
	if token != "" || scope != "" {
		deployCommand = strings.TrimSpace(deployCommand)
		if scope != "" && !strings.Contains(deployCommand, "--scope") {
			deployCommand += " --scope " + shellQuote(scope)
		}
		if token != "" && !strings.Contains(deployCommand, "--token") {
			deployCommand += " --token " + shellQuote(token)
		}
	}
	if !runShellStep(ctx, profile, result, "deploy", deployCommand, func(output string) {
		result.DeploymentURL = extractDeploymentURL(output)
	}) {
		return
	}
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
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func hasVercelLink(repoPath string) bool {
	_, err := os.Stat(filepath.Join(repoPath, ".vercel", "project.json"))
	return err == nil
}

func runShellStep(ctx context.Context, profile DeploymentProfile, result *DeploymentRunResult, name, command string, after func(string)) bool {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", command) //nolint:gosec
	cmd.Dir = profile.RepoPath
	return executeStep(result, name, command, cmd, after)
}

func runCommandStep(ctx context.Context, profile DeploymentProfile, result *DeploymentRunResult, name, bin string, args ...string) bool {
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec
	cmd.Dir = profile.RepoPath
	return executeStep(result, name, strings.Join(append([]string{bin}, args...), " "), cmd, nil)
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

func secretValue(ctx context.Context, provider secret.Provider, service, key string) string {
	if provider == nil {
		return ""
	}
	value, _ := provider.Get(ctx, service, key)
	return strings.TrimSpace(value)
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
