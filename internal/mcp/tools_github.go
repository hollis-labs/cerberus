package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// NewCerberusGithubStatusTool creates the cerberus_github_status tool.
func NewCerberusGithubStatusTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_github_status",
		Description: "Get a GitHub repository summary.",
		InputSchema: githubRepoSchema(false),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			owner, _ := args["owner"].(string)
			repo, _ := args["repo"].(string)
			return executeGitHubMCP(ctx, client, "status", owner, repo, 0)
		},
	})
}

// NewCerberusGithubReleasesTool creates the cerberus_github_releases tool.
func NewCerberusGithubReleasesTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_github_releases",
		Description: "List recent releases for a GitHub repo.",
		InputSchema: githubRepoSchema(true),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			owner, _ := args["owner"].(string)
			repo, _ := args["repo"].(string)
			return executeGitHubMCP(ctx, client, "list_releases", owner, repo, intArg(args, "limit", 10))
		},
	})
}

// NewCerberusGithubRunsTool creates the cerberus_github_runs tool.
func NewCerberusGithubRunsTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_github_runs",
		Description: "List recent GitHub Actions runs for a repo.",
		InputSchema: githubRepoSchema(true),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			owner, _ := args["owner"].(string)
			repo, _ := args["repo"].(string)
			return executeGitHubMCP(ctx, client, "list_workflow_runs", owner, repo, intArg(args, "limit", 10))
		},
	})
}

func executeGitHubMCP(ctx context.Context, client cerbapi.Client, operation, owner, repo string, limit int) (any, error) {
	cfg := map[string]any{
		"owner": owner,
		"repo":  repo,
	}
	if limit > 0 {
		cfg["limit"] = limit
	}
	result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
		Connector: "github",
		Operation: operation,
		Config:    cfg,
	})
	if err != nil {
		return toolResult(lifecycleResult{Success: false, Error: err.Error()})
	}

	data, err := redact.MarshalIndent(result.Data, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func githubRepoSchema(withLimit bool) map[string]interface{} {
	properties := map[string]interface{}{
		"owner": map[string]interface{}{
			"type":        "string",
			"description": "Repository owner (user or org).",
		},
		"repo": map[string]interface{}{
			"type":        "string",
			"description": "Repository name.",
		},
	}
	if withLimit {
		properties["limit"] = map[string]interface{}{
			"type":        "integer",
			"description": "Max records to return (default 10).",
		}
	}
	return map[string]interface{}{
		"type":                 "object",
		"properties":           properties,
		"required":             []string{"owner", "repo"},
		"additionalProperties": false,
	}
}

func intArg(args map[string]interface{}, key string, fallback int) int {
	if value, ok := args[key].(float64); ok && value > 0 {
		return int(value)
	}
	return fallback
}
