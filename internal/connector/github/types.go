package github

import "time"

// RepoStatus is the normalized view of a GitHub repository's current state.
type RepoStatus struct {
	Owner       string    `json:"owner"`
	Repo        string    `json:"repo"`
	Description string    `json:"description,omitempty"`
	DefaultBr   string    `json:"default_branch"`
	Private     bool      `json:"private"`
	Stars       int       `json:"stars"`
	OpenIssues  int       `json:"open_issues"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Release is the normalized view of a GitHub release.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
}

// WorkflowRun is the normalized view of a GitHub Actions workflow run.
type WorkflowRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`     // queued, in_progress, completed
	Conclusion string    `json:"conclusion"` // success, failure, canceled, etc.
	Branch     string    `json:"branch"`
	Event      string    `json:"event"`
	CreatedAt  time.Time `json:"created_at"`
	HTMLURL    string    `json:"html_url"`
}
