package github

import (
	"context"
	"testing"

	"github.com/hollis-labs/cerberus/internal/domain"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

type fakeBackend struct {
	owner string
	repo  string
}

func (b *fakeBackend) RepoStatus(_ context.Context, owner, repo string) (*RepoStatus, error) {
	b.owner = owner
	b.repo = repo
	return &RepoStatus{Owner: owner, Repo: repo}, nil
}

func (b *fakeBackend) ListReleases(_ context.Context, _ string, _ string, _ int) ([]Release, error) {
	return nil, nil
}

func (b *fakeBackend) ListWorkflowRuns(_ context.Context, _ string, _ string, _ int) ([]WorkflowRun, error) {
	return nil, nil
}

func TestGitHubConnectorStatusUsesResourceConfig(t *testing.T) {
	backend := &fakeBackend{}
	conn := NewWithBackend(backend)
	res := &domain.Resource{
		ID:     "repo",
		Config: map[string]any{"owner": "hollis-labs", "repo": "cerberus"},
	}

	state, err := conn.Status(context.Background(), res)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if state != domain.StateRunning {
		t.Fatalf("state = %q, want %q", state, domain.StateRunning)
	}
	if backend.owner != "hollis-labs" || backend.repo != "cerberus" {
		t.Fatalf("backend called with %q/%q", backend.owner, backend.repo)
	}
}

func TestGitHubConnectorDefinition(t *testing.T) {
	conn := NewWithBackend(&fakeBackend{})
	def := conn.Definition()

	if def.ID != "github" {
		t.Fatalf("ID = %q, want github", def.ID)
	}
	if len(def.ResourceTypes) != 1 || def.ResourceTypes[0] != "repository" {
		t.Fatalf("ResourceTypes = %#v, want [repository]", def.ResourceTypes)
	}
	if !def.Capabilities.CanHealth {
		t.Fatalf("Capabilities = %#v, want health", def.Capabilities)
	}
	if len(def.Config.Secrets) != 1 || def.Config.Secrets[0].Env != "CERBERUS_GITHUB_TOKEN" {
		t.Fatalf("Secrets = %#v, want GitHub token env", def.Config.Secrets)
	}
	if len(def.Operations) == 0 {
		t.Fatal("expected operations")
	}

	var _ contract.Describer = conn
}
