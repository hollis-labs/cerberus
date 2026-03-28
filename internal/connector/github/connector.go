package github

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chrispian/cerberus/internal/domain"
)

// Connector manages GitHub resources (repositories, releases) via either
// the go-github API SDK or the gh CLI, selected automatically or by config.
type Connector struct {
	backend Backend
}

// New creates a GitHub connector. It tries the API backend first (if a token
// is available via secrets), then falls back to the gh CLI.
func New(secrets domain.SecretProvider) (*Connector, error) {
	// Try API backend first
	if secrets != nil {
		token, _ := secrets.Get(context.Background(), "github", "token")
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
func (c *Connector) ResourceTypes() []string { return []string{"repository"} }

func (c *Connector) Capabilities() domain.ConnectorCapabilities {
	return domain.ConnectorCapabilities{
		CanCreate:  false, // read-only for now
		CanDestroy: false,
		CanBuild:   false,
		CanLogs:    false,
		CanHealth:  true,
	}
}

func (c *Connector) Create(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("github connector does not support Create")
}

func (c *Connector) Start(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("github connector does not support Start")
}

func (c *Connector) Stop(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("github connector does not support Stop")
}

func (c *Connector) Destroy(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("github connector does not support Destroy")
}

// Status returns the repository's current state by querying GitHub.
func (c *Connector) Status(ctx context.Context, res *domain.Resource) (domain.State, error) {
	owner, repo, err := parseOwnerRepo(res)
	if err != nil {
		return domain.StateUnknown, err
	}

	_, err = c.backend.RepoStatus(ctx, owner, repo)
	if err != nil {
		return domain.StateUnknown, fmt.Errorf("github status: %w", err)
	}

	return domain.StateRunning, nil
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
func parseOwnerRepo(res *domain.Resource) (string, string, error) {
	owner, _ := res.Config["owner"].(string)
	repo, _ := res.Config["repo"].(string)
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("github resource %q missing owner or repo in config", res.ID)
	}
	return owner, repo, nil
}
