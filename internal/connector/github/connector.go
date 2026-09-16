package github

import (
	"context"
	"encoding/json"
	"fmt"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

var _ contract.Connector = (*Connector)(nil)
var _ contract.Describer = (*Connector)(nil)

// Connector manages GitHub resources (repositories, releases) via either
// the go-github API SDK or the gh CLI, selected automatically or by config.
type Connector struct {
	backend Backend
}

// New creates a GitHub connector. It tries the API backend first (if a token
// is available via secrets), then falls back to the gh CLI.
func New(secrets secret.Provider) (*Connector, error) {
	// Try API backend first
	if secrets != nil {
		token, err := secrets.Get(context.Background(), "github", "token")
		if err != nil {
			return nil, fmt.Errorf("github: resolve credential: %w", err)
		}
		if token != "" {
			return &Connector{backend: NewAPIBackend(token)}, nil
		}
	}

	// Fall back to CLI
	if path, ok := DetectGH(); ok {
		return &Connector{backend: newCLIBackendWithPath(path)}, nil
	}

	return nil, fmt.Errorf("github connector: no API token and gh CLI not found — set CERBERUS_GITHUB_TOKEN or install gh")
}

// NewWithBackend creates a GitHub connector with an explicit backend.
func NewWithBackend(b Backend) *Connector {
	return &Connector{backend: b}
}

func (c *Connector) ID() string              { return "github" }
func (c *Connector) ResourceTypes() []string { return []string{string(resource.Repo)} }

func (c *Connector) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		CanCreate:  false, // read-only for now
		CanDestroy: false,
		CanBuild:   false,
		CanLogs:    false,
		CanHealth:  true,
	}
}

func Definition() contract.Definition {
	return contract.Definition{
		ID:            "github",
		Version:       "builtin",
		ResourceTypes: []string{string(resource.Repo)},
		Capabilities: contract.Capabilities{
			CanCreate:  false,
			CanDestroy: false,
			CanBuild:   false,
			CanLogs:    false,
			CanHealth:  true,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{
					Name:        "owner",
					Type:        "string",
					Description: "GitHub repository owner or organization.",
					Required:    true,
				},
				{
					Name:        "repo",
					Type:        "string",
					Description: "GitHub repository name.",
					Required:    true,
				},
			},
			Secrets: []contract.SecretRequirement{
				{
					Name:        "token",
					Description: "GitHub API token used when the API backend is available.",
					Env:         "CERBERUS_GITHUB_TOKEN",
				},
			},
		},
		Operations: []contract.Operation{
			{
				Name:        "status",
				Description: "Read repository status.",
				InputSchema: githubRepoInputSchema(),
			},
			{
				Name:        "list_releases",
				Description: "List recent repository releases.",
				InputSchema: githubRepoLimitInputSchema(),
			},
			{
				Name:        "list_workflow_runs",
				Description: "List recent GitHub Actions workflow runs.",
				InputSchema: githubRepoLimitInputSchema(),
			},
		},
	}
}

func (c *Connector) Definition() contract.Definition {
	return Definition()
}

func (c *Connector) Create(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("github connector does not support Create")
}

func (c *Connector) Start(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("github connector does not support Start")
}

func (c *Connector) Stop(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("github connector does not support Stop")
}

func (c *Connector) Destroy(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("github connector does not support Destroy")
}

// Status returns the repository's current state by querying GitHub.
func (c *Connector) Status(ctx context.Context, res *resource.Resource) (resource.State, error) {
	owner, repo, err := parseOwnerRepo(res)
	if err != nil {
		return resource.StateUnknown, err
	}

	_, err = c.backend.RepoStatus(ctx, owner, repo)
	if err != nil {
		return resource.StateUnknown, fmt.Errorf("github status: %w", err)
	}

	return resource.StateRunning, nil
}

// RepoStatus returns detailed repository status.
func (c *Connector) RepoStatus(ctx context.Context, owner, repo string) (*RepoStatus, error) {
	return c.backend.RepoStatus(ctx, owner, repo)
}

// ListReleases returns recent releases for a repository.
func (c *Connector) ListReleases(ctx context.Context, owner, repo string, limit int) ([]Release, error) {
	if limit <= 0 {
		limit = 10
	}
	return c.backend.ListReleases(ctx, owner, repo, limit)
}

// ListWorkflowRuns returns recent workflow runs for a repository.
func (c *Connector) ListWorkflowRuns(ctx context.Context, owner, repo string, limit int) ([]WorkflowRun, error) {
	if limit <= 0 {
		limit = 10
	}
	return c.backend.ListWorkflowRuns(ctx, owner, repo, limit)
}

// StatusJSON returns the full repo status as a JSON string (used by MCP tools).
func (c *Connector) StatusJSON(ctx context.Context, owner, repo string) (string, error) {
	status, err := c.backend.RepoStatus(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ReleasesJSON returns releases as a JSON string (used by MCP tools).
func (c *Connector) ReleasesJSON(ctx context.Context, owner, repo string, limit int) (string, error) {
	releases, err := c.ListReleases(ctx, owner, repo, limit)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(releases, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// parseOwnerRepo extracts owner and repo from a resource's config.
func parseOwnerRepo(res *resource.Resource) (string, string, error) {
	owner, _ := res.Config["owner"].(string)
	repo, _ := res.Config["repo"].(string)
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("github resource %q missing owner or repo in config", res.ID)
	}
	return owner, repo, nil
}

func githubRepoInputSchema() map[string]any {
	return contract.ObjectSchema(map[string]any{
		"owner": contract.StringSchema("GitHub repository owner or organization."),
		"repo":  contract.StringSchema("GitHub repository name."),
	}, "owner", "repo")
}

func githubRepoLimitInputSchema() map[string]any {
	return contract.ObjectSchema(map[string]any{
		"owner": contract.StringSchema("GitHub repository owner or organization."),
		"repo":  contract.StringSchema("GitHub repository name."),
		"limit": contract.IntegerSchema("Maximum number of records to return."),
	}, "owner", "repo")
}
