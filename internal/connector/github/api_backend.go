package github

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v72/github"
)

// APIBackend implements Backend using the go-github SDK.
type APIBackend struct {
	client *gh.Client
}

// NewAPIBackend creates an APIBackend authenticated with the given personal access token.
func NewAPIBackend(token string) *APIBackend {
	return &APIBackend{
		client: gh.NewClient(nil).WithAuthToken(token),
	}
}

func (a *APIBackend) RepoStatus(ctx context.Context, owner, repo string) (*RepoStatus, error) {
	r, _, err := a.client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("get repo %s/%s: %w", owner, repo, err)
	}
	return &RepoStatus{
		Owner:       owner,
		Repo:        repo,
		Description: r.GetDescription(),
		DefaultBr:   r.GetDefaultBranch(),
		Private:     r.GetPrivate(),
		Stars:       r.GetStargazersCount(),
		OpenIssues:  r.GetOpenIssuesCount(),
		UpdatedAt:   r.GetUpdatedAt().Time,
	}, nil
}

func (a *APIBackend) ListReleases(ctx context.Context, owner, repo string, limit int) ([]Release, error) {
	releases, _, err := a.client.Repositories.ListReleases(ctx, owner, repo, &gh.ListOptions{
		PerPage: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list releases %s/%s: %w", owner, repo, err)
	}
	out := make([]Release, 0, len(releases))
	for _, r := range releases {
		out = append(out, Release{
			TagName:     r.GetTagName(),
			Name:        r.GetName(),
			Draft:       r.GetDraft(),
			Prerelease:  r.GetPrerelease(),
			PublishedAt: r.GetPublishedAt().Time,
			HTMLURL:     r.GetHTMLURL(),
		})
	}
	return out, nil
}

func (a *APIBackend) ListWorkflowRuns(ctx context.Context, owner, repo string, limit int) ([]WorkflowRun, error) {
	runs, _, err := a.client.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, &gh.ListWorkflowRunsOptions{
		ListOptions: gh.ListOptions{PerPage: limit},
	})
	if err != nil {
		return nil, fmt.Errorf("list workflow runs %s/%s: %w", owner, repo, err)
	}
	out := make([]WorkflowRun, 0, len(runs.WorkflowRuns))
	for _, r := range runs.WorkflowRuns {
		out = append(out, WorkflowRun{
			ID:         r.GetID(),
			Name:       r.GetName(),
			Status:     r.GetStatus(),
			Conclusion: r.GetConclusion(),
			Branch:     r.GetHeadBranch(),
			Event:      r.GetEvent(),
			CreatedAt:  r.GetCreatedAt().Time,
			HTMLURL:    r.GetHTMLURL(),
		})
	}
	return out, nil
}
