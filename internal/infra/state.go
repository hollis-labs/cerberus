package infra

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chrispian/cerberus/internal/config"
	"gopkg.in/yaml.v3"
)

const stateVersion = 1

type State struct {
	Version   int                       `yaml:"version"`
	Providers map[string]ProviderConfig `yaml:"providers,omitempty"`
	Profiles  []DeploymentProfile       `yaml:"profiles,omitempty"`
}

type ProviderConfig struct {
	Values map[string]string `yaml:"values,omitempty"`
}

type DeploymentProfile struct {
	ID               string `yaml:"id"`
	Name             string `yaml:"name"`
	Provider         string `yaml:"provider"`
	RepoPath         string `yaml:"repo_path"`
	Domain           string `yaml:"domain,omitempty"`
	DNSProvider      string `yaml:"dns_provider,omitempty"`
	ProductionBranch string `yaml:"production_branch,omitempty"`
	GitRemote        string `yaml:"git_remote,omitempty"`
	GitProvider      string `yaml:"git_provider,omitempty"`
	GitOwner         string `yaml:"git_owner,omitempty"`
	GitRepo          string `yaml:"git_repo,omitempty"`
	VercelProject    string `yaml:"vercel_project,omitempty"`
	VercelScope      string `yaml:"vercel_scope,omitempty"`
	CloudflareZoneID string `yaml:"cloudflare_zone_id,omitempty"`
	NamecheapDomain  string `yaml:"namecheap_domain,omitempty"`
	PreflightCommand string `yaml:"preflight_command,omitempty"`
	BuildCommand     string `yaml:"build_command,omitempty"`
	DeployCommand    string `yaml:"deploy_command,omitempty"`
}

func StatePathFor(configPath string) (string, error) {
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	return filepath.Join(filepath.Dir(configPath), "infra.yaml"), nil
}

func LoadState(configPath string) (*State, error) {
	path, err := StatePathFor(configPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return &State{Version: stateVersion, Providers: map[string]ProviderConfig{}}, nil
		}
		return nil, fmt.Errorf("read infra state %s: %w", path, err)
	}
	var state State
	if err := yaml.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse infra state %s: %w", path, err)
	}
	if state.Version == 0 {
		state.Version = stateVersion
	}
	if state.Providers == nil {
		state.Providers = map[string]ProviderConfig{}
	}
	return &state, nil
}

func SaveState(configPath string, state *State) error {
	path, err := StatePathFor(configPath)
	if err != nil {
		return err
	}
	if state == nil {
		return fmt.Errorf("infra state is nil")
	}
	state.Version = stateVersion
	if state.Providers == nil {
		state.Providers = map[string]ProviderConfig{}
	}
	data, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal infra state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create infra dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write infra state %s: %w", path, err)
	}
	return nil
}

func (s *State) UpsertProfile(profile DeploymentProfile) {
	for i := range s.Profiles {
		if s.Profiles[i].ID == profile.ID {
			s.Profiles[i] = profile
			return
		}
	}
	s.Profiles = append(s.Profiles, profile)
}

func (s *State) DeleteProfile(id string) bool {
	for i := range s.Profiles {
		if s.Profiles[i].ID == id {
			s.Profiles = append(s.Profiles[:i], s.Profiles[i+1:]...)
			return true
		}
	}
	return false
}

func (s *State) Profile(id string) (DeploymentProfile, bool) {
	for _, profile := range s.Profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return DeploymentProfile{}, false
}

func SuggestedProfiles() []DeploymentProfile {
	suggestions := []DeploymentProfile{}
	chrispianPath := "/Users/chrispian/dev/sites/chrispian.dev"
	if info, err := os.Stat(chrispianPath); err == nil && info.IsDir() {
		gitRemote, gitOwner, gitRepo := suggestedGitMetadata(chrispianPath)
		suggestions = append(suggestions, DeploymentProfile{
			ID:               "chrispian-dev",
			Name:             "chrispian.dev",
			Provider:         "vercel",
			RepoPath:         chrispianPath,
			Domain:           "chrispian.dev",
			DNSProvider:      "namecheap",
			ProductionBranch: "main",
			GitProvider:      "github",
			GitRemote:        gitRemote,
			GitOwner:         gitOwner,
			GitRepo:          gitRepo,
			PreflightCommand: "pnpm check",
			BuildCommand:     "pnpm build",
			DeployCommand:    "vercel --prod --yes",
		})
	}
	return suggestions
}

func suggestedGitMetadata(repoPath string) (remote, owner, repo string) {
	remotes := strings.Fields(runGitCommand(repoPath, "remote"))
	for _, candidate := range []string{"origin"} {
		for _, remoteName := range remotes {
			if remoteName != candidate {
				continue
			}
			url := strings.TrimSpace(runGitCommand(repoPath, "remote", "get-url", remoteName))
			owner, repo = parseGitHubRemote(url)
			return remoteName, owner, repo
		}
	}
	return "", "", ""
}

func parseGitHubRemote(raw string) (owner, repo string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	switch {
	case strings.HasPrefix(raw, "git@github.com:"):
		raw = strings.TrimPrefix(raw, "git@github.com:")
	case strings.HasPrefix(raw, "https://github.com/"):
		raw = strings.TrimPrefix(raw, "https://github.com/")
	default:
		return "", ""
	}
	raw = strings.TrimSuffix(raw, ".git")
	parts := strings.Split(raw, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

func runGitCommand(repoPath string, args ...string) string {
	cmd := exec.Command("git", args...) //nolint:gosec
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}
