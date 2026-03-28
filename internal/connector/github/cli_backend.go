package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// CLIBackend implements Backend by shelling out to the gh CLI.
type CLIBackend struct {
	ghPath string
}

// DetectGH returns the path to the gh binary and whether it was found.
func DetectGH() (string, bool) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return "", false
	}
	return path, true
}

// NewCLIBackend creates a CLIBackend after verifying that gh is available in PATH.
func NewCLIBackend() (*CLIBackend, error) {
	path, found := DetectGH()
	if !found {
		return nil, fmt.Errorf("gh CLI not found in PATH")
	}
	return &CLIBackend{ghPath: path}, nil
}

// newCLIBackendWithPath creates a CLIBackend using an already-resolved gh path.
func newCLIBackendWithPath(path string) *CLIBackend {
	return &CLIBackend{ghPath: path}
}

// --- intermediate JSON structs for gh CLI output ---

type ghRepoView struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	DefaultBranchRef struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
	IsPrivate      bool      `json:"isPrivate"`
	StargazerCount int       `json:"stargazerCount"`
	Issues         ghIssues  `json:"issues"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type ghIssues struct {
	TotalCount int `json:"totalCount"`
}

type ghRelease struct {
	TagName      string    `json:"tagName"`
	Name         string    `json:"name"`
	IsDraft      bool      `json:"isDraft"`
	IsPrerelease bool      `json:"isPrerelease"`
	PublishedAt  time.Time `json:"publishedAt"`
	URL          string    `json:"url"`
}

type ghWorkflowRun struct {
	DatabaseID int64     `json:"databaseId"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HeadBranch string    `json:"headBranch"`
	Event      string    `json:"event"`
	CreatedAt  time.Time `json:"createdAt"`
	URL        string    `json:"url"`
}

// RepoStatus returns the current state of a repository via gh repo view.
func (c *CLIBackend) RepoStatus(ctx context.Context, owner, repo string) (*RepoStatus, error) {
	slug := owner + "/" + repo
	out, err := c.run(ctx, "repo", "view", slug,
		"--json", "name,description,defaultBranchRef,isPrivate,stargazerCount,issues,updatedAt")
	if err != nil {
		return nil, fmt.Errorf("gh repo view %s: %w", slug, err)
	}

	var rv ghRepoView
	if err := json.Unmarshal(out, &rv); err != nil {
		return nil, fmt.Errorf("parsing repo view JSON: %w", err)
	}

	return &RepoStatus{
		Owner:       owner,
		Repo:        repo,
		Description: rv.Description,
		DefaultBr:   rv.DefaultBranchRef.Name,
		Private:     rv.IsPrivate,
		Stars:       rv.StargazerCount,
		OpenIssues:  rv.Issues.TotalCount,
		UpdatedAt:   rv.UpdatedAt,
	}, nil
}

// ListReleases returns recent releases for a repository via gh release list.
func (c *CLIBackend) ListReleases(ctx context.Context, owner, repo string, limit int) ([]Release, error) {
	slug := owner + "/" + repo
	out, err := c.run(ctx, "release", "list",
		"--repo", slug,
		"--limit", strconv.Itoa(limit),
		"--json", "tagName,name,isDraft,isPrerelease,publishedAt,url")
	if err != nil {
		return nil, fmt.Errorf("gh release list %s: %w", slug, err)
	}

	var ghr []ghRelease
	if err := json.Unmarshal(out, &ghr); err != nil {
		return nil, fmt.Errorf("parsing release list JSON: %w", err)
	}

	releases := make([]Release, len(ghr))
	for i, r := range ghr {
		releases[i] = Release{
			TagName:     r.TagName,
			Name:        r.Name,
			Draft:       r.IsDraft,
			Prerelease:  r.IsPrerelease,
			PublishedAt: r.PublishedAt,
			HTMLURL:     r.URL,
		}
	}
	return releases, nil
}

// ListWorkflowRuns returns recent workflow runs for a repository via gh run list.
func (c *CLIBackend) ListWorkflowRuns(ctx context.Context, owner, repo string, limit int) ([]WorkflowRun, error) {
	slug := owner + "/" + repo
	out, err := c.run(ctx, "run", "list",
		"--repo", slug,
		"--limit", strconv.Itoa(limit),
		"--json", "databaseId,name,status,conclusion,headBranch,event,createdAt,url")
	if err != nil {
		return nil, fmt.Errorf("gh run list %s: %w", slug, err)
	}

	var ghr []ghWorkflowRun
	if err := json.Unmarshal(out, &ghr); err != nil {
		return nil, fmt.Errorf("parsing workflow run list JSON: %w", err)
	}

	runs := make([]WorkflowRun, len(ghr))
	for i, r := range ghr {
		runs[i] = WorkflowRun{
			ID:         r.DatabaseID,
			Name:       r.Name,
			Status:     r.Status,
			Conclusion: r.Conclusion,
			Branch:     r.HeadBranch,
			Event:      r.Event,
			CreatedAt:  r.CreatedAt,
			HTMLURL:    r.URL,
		}
	}
	return runs, nil
}

// run executes a gh subcommand and returns its stdout bytes.
func (c *CLIBackend) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.ghPath, args...) //nolint:gosec
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok { //nolint:errorlint
			return nil, fmt.Errorf("%w: %s", err, string(exitErr.Stderr))
		}
		return nil, err
	}
	return out, nil
}
