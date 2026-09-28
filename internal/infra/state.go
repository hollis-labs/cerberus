package infra

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/target"
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

	// Env, Owner, Admin and Tags label the profile's target for policy, as
	// a resource's do (Decision 18). Unlabeled reads as unknown, which is
	// strict: it needs out-of-band approval wherever policy asks for one.
	Env   target.Env   `yaml:"env,omitempty"`
	Owner string       `yaml:"owner,omitempty"`
	Admin target.Admin `yaml:"admin,omitempty"`
	Tags  []string     `yaml:"tags,omitempty"`
}

// ResourceDef is the profile as the resource its target is resolved
// through: its labels, under its id.
func (p DeploymentProfile) ResourceDef() config.ResourceDef {
	return config.ResourceDef{ID: p.ID, Env: p.Env, Owner: p.Owner, Admin: p.Admin, Tags: append([]string(nil), p.Tags...)}
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
