package mcp

import (
	"context"
	"encoding/json"

	forgeconn "github.com/chrispian/cerberus/internal/connector/forge"
	"github.com/chrispian/cerberus/internal/domain"
)

// NewCerberusForgeServersTool creates the cerberus_forge_servers tool.
func NewCerberusForgeServersTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_forge_servers",
		Description: "Lists all Laravel Forge servers on the account.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			conn, err := forgeconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			servers, err := conn.ListServers(context.Background())
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			data, err := json.MarshalIndent(servers, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// NewCerberusForgeServerTool creates the cerberus_forge_server tool.
func NewCerberusForgeServerTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_forge_server",
		Description: "Returns details for a specific Laravel Forge server.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"server_id": map[string]interface{}{
					"type":        "integer",
					"description": "Forge server ID.",
				},
			},
			"required": []string{"server_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			serverID := 0
			if id, ok := args["server_id"].(float64); ok {
				serverID = int(id)
			}
			if serverID == 0 {
				return marshalResult(lifecycleResult{Success: false, Error: "server_id is required"}), nil
			}

			conn, err := forgeconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			server, err := conn.GetServer(context.Background(), serverID)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			data, err := json.MarshalIndent(server, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// NewCerberusForgeSitesTool creates the cerberus_forge_sites tool.
func NewCerberusForgeSitesTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_forge_sites",
		Description: "Lists all sites on a Laravel Forge server.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"server_id": map[string]interface{}{
					"type":        "integer",
					"description": "Forge server ID.",
				},
			},
			"required": []string{"server_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			serverID := 0
			if id, ok := args["server_id"].(float64); ok {
				serverID = int(id)
			}
			if serverID == 0 {
				return marshalResult(lifecycleResult{Success: false, Error: "server_id is required"}), nil
			}

			conn, err := forgeconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			sites, err := conn.ListSites(context.Background(), serverID)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			data, err := json.MarshalIndent(sites, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}
