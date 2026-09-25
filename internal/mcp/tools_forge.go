package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// NewCerberusForgeServersTool creates the cerberus_forge_servers tool.
func NewCerberusForgeServersTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_forge_servers",
		Description: "List Laravel Forge servers.",
		InputSchema: emptyObjectSchema(),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return executeConnectorMCP(ctx, client, "forge", "list_servers", nil, false, false)
		},
	})
}

// NewCerberusForgeServerTool creates the cerberus_forge_server tool.
func NewCerberusForgeServerTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_forge_server",
		Description: "Get details for one Laravel Forge server.",
		InputSchema: objectSchema(map[string]interface{}{
			"server_id": map[string]interface{}{"type": "integer", "description": "Forge server ID."},
		}, "server_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return executeConnectorMCP(ctx, client, "forge", "get_server", map[string]any{"server_id": intArg(args, "server_id", 0)}, false, false)
		},
	})
}

// NewCerberusForgeSitesTool creates the cerberus_forge_sites tool.
func NewCerberusForgeSitesTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_forge_sites",
		Description: "List sites on a Laravel Forge server.",
		InputSchema: objectSchema(map[string]interface{}{
			"server_id": map[string]interface{}{"type": "integer", "description": "Forge server ID."},
		}, "server_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return executeConnectorMCP(ctx, client, "forge", "list_sites", map[string]any{"server_id": intArg(args, "server_id", 0)}, false, false)
		},
	})
}

// NewCerberusForgeDeployTool creates the cerberus_forge_deploy tool.
func NewCerberusForgeDeployTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_forge_deploy",
		Description: "Deploy a Laravel Forge site.",
		InputSchema: objectSchema(map[string]interface{}{
			"server_id":    map[string]interface{}{"type": "integer", "description": "Forge server ID."},
			"site_id":      map[string]interface{}{"type": "integer", "description": "Forge site ID."},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "server_id", "site_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return executeConnectorMCP(ctx, client, "forge", "deploy_site", map[string]any{
				"server_id": intArg(args, "server_id", 0),
				"site_id":   intArg(args, "site_id", 0),
			}, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	})
}

// NewCerberusForgeExecTool creates the cerberus_forge_exec tool.
func NewCerberusForgeExecTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_forge_exec",
		Description: "Run a command on a Laravel Forge site.",
		InputSchema: objectSchema(map[string]interface{}{
			"server_id":    map[string]interface{}{"type": "integer", "description": "Forge server ID."},
			"site_id":      map[string]interface{}{"type": "integer", "description": "Forge site ID."},
			"command":      map[string]interface{}{"type": "string", "description": "Command to run."},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "server_id", "site_id", "command"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return executeConnectorMCP(ctx, client, "forge", "exec_site_command", map[string]any{
				"server_id": intArg(args, "server_id", 0),
				"site_id":   intArg(args, "site_id", 0),
				"command":   stringArg(args, "command"),
			}, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	})
}
