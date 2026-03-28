package github

import "context"

// Backend defines how the GitHub connector communicates with GitHub.
// Two implementations: APIBackend (go-github SDK) and CLIBackend (gh CLI).
type Backend interface {
	// RepoStatus returns the current state of a repository.
	RepoStatus(ctx context.Context, owner, repo string) (*RepoStatus, error)

	// ListReleases returns recent releases for a repository.
	ListReleases(ctx context.Context, owner, repo string, limit int) ([]Release, error)

	// ListWorkflowRuns returns recent workflow runs for a repository.
	ListWorkflowRuns(ctx context.Context, owner, repo string, limit int) ([]WorkflowRun, error)
}
