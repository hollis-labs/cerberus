package mcp

import "github.com/chrispian/cerberus/internal/cerbapi"

// NewCerberusForgeServersTool creates the cerberus_forge_servers tool.
func NewCerberusForgeServersTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_forge_servers",
		Description: "Lists all Laravel Forge servers on the account.",
		InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "forge", "list_servers", nil, false, false)
		},
	}
}

// NewCerberusForgeServerTool creates the cerberus_forge_server tool.
func NewCerberusForgeServerTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_forge_server",
		Description: "Returns details for a specific Laravel Forge server.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"server_id": map[string]interface{}{"type": "integer", "description": "Forge server ID."},
			},
			"required": []string{"server_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "forge", "get_server", map[string]any{"server_id": intArg(args, "server_id", 0)}, false, false)
		},
	}
}

// NewCerberusForgeSitesTool creates the cerberus_forge_sites tool.
func NewCerberusForgeSitesTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_forge_sites",
		Description: "Lists all sites on a Laravel Forge server.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"server_id": map[string]interface{}{"type": "integer", "description": "Forge server ID."},
			},
			"required": []string{"server_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "forge", "list_sites", map[string]any{"server_id": intArg(args, "server_id", 0)}, false, false)
		},
	}
}

// NewCerberusForgeDeployTool creates the cerberus_forge_deploy tool.
func NewCerberusForgeDeployTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_forge_deploy",
		Description: "Triggers a deployment for a Forge site.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"server_id":    map[string]interface{}{"type": "integer", "description": "Forge server ID."},
				"site_id":      map[string]interface{}{"type": "integer", "description": "Forge site ID."},
				"dry_run":      map[string]interface{}{"type": "boolean", "description": "Set true to preview the deployment without triggering it."},
				"acknowledged": map[string]interface{}{"type": "boolean", "description": "Set true to acknowledge this destructive deployment action."},
			},
			"required": []string{"server_id", "site_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "forge", "deploy_site", map[string]any{
				"server_id": intArg(args, "server_id", 0),
				"site_id":   intArg(args, "site_id", 0),
			}, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}

// NewCerberusForgeExecTool creates the cerberus_forge_exec tool.
func NewCerberusForgeExecTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_forge_exec",
		Description: "Executes a command on a Forge site.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"server_id":    map[string]interface{}{"type": "integer", "description": "Forge server ID."},
				"site_id":      map[string]interface{}{"type": "integer", "description": "Forge site ID."},
				"command":      map[string]interface{}{"type": "string", "description": "Command to execute."},
				"dry_run":      map[string]interface{}{"type": "boolean", "description": "Set true to preview the remote command without executing it."},
				"acknowledged": map[string]interface{}{"type": "boolean", "description": "Set true to acknowledge this destructive remote command."},
			},
			"required": []string{"server_id", "site_id", "command"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "forge", "exec_site_command", map[string]any{
				"server_id": intArg(args, "server_id", 0),
				"site_id":   intArg(args, "site_id", 0),
				"command":   stringArg(args, "command"),
			}, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}
