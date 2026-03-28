package mcp

import (
	"context"
	"encoding/json"

	ghconn "github.com/chrispian/cerberus/internal/connector/github"
	"github.com/chrispian/cerberus/internal/domain"
)

// NewCerberusGithubStatusTool creates the cerberus_github_status tool.
func NewCerberusGithubStatusTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_github_status",
		Description: "Returns the current status of a GitHub repository including stars, open issues, and last update time.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{
					"type":        "string",
					"description": "Repository owner (user or org).",
				},
				"repo": map[string]interface{}{
					"type":        "string",
					"description": "Repository name.",
				},
			},
			"required": []string{"owner", "repo"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			owner, _ := args["owner"].(string)
			repo, _ := args["repo"].(string)

			gh, err := ghconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return gh.StatusJSON(context.Background(), owner, repo)
		},
	}
}

// NewCerberusGithubReleasesTool creates the cerberus_github_releases tool.
func NewCerberusGithubReleasesTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_github_releases",
		Description: "Lists recent releases for a GitHub repository.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{
					"type":        "string",
					"description": "Repository owner (user or org).",
				},
				"repo": map[string]interface{}{
					"type":        "string",
					"description": "Repository name.",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Max releases to return (default 10).",
				},
			},
			"required": []string{"owner", "repo"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			owner, _ := args["owner"].(string)
			repo, _ := args["repo"].(string)
			limit := 10
			if l, ok := args["limit"].(float64); ok && l > 0 {
				limit = int(l)
			}

			gh, err := ghconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return gh.ReleasesJSON(context.Background(), owner, repo, limit)
		},
	}
}

// NewCerberusGithubRunsTool creates the cerberus_github_runs tool.
func NewCerberusGithubRunsTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_github_runs",
		Description: "Lists recent GitHub Actions workflow runs for a repository.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{
					"type":        "string",
					"description": "Repository owner (user or org).",
				},
				"repo": map[string]interface{}{
					"type":        "string",
					"description": "Repository name.",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Max runs to return (default 10).",
				},
			},
			"required": []string{"owner", "repo"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			owner, _ := args["owner"].(string)
			repo, _ := args["repo"].(string)
			limit := 10
			if l, ok := args["limit"].(float64); ok && l > 0 {
				limit = int(l)
			}

			gh, err := ghconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			runs, err := gh.ListWorkflowRuns(context.Background(), owner, repo, limit)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			data, err := json.MarshalIndent(runs, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}
