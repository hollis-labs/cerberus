package pluginhost

import (
	"encoding/json"
	"fmt"

	contract "github.com/chrispian/cerberus/pkg/connector"
)

const (
	SDKProtocolVersion = 1

	SDKMethodInit        = "plugin/init"
	SDKMethodLoad        = "plugin/load"
	SDKMethodUnload      = "plugin/unload"
	SDKMethodHealth      = "plugin/health"
	SDKMethodMCPCallTool = "mcp/call_tool"
)

type SDKInitParams struct {
	PluginDir string            `json:"plugin_dir"`
	DataDir   string            `json:"data_dir"`
	CacheDir  string            `json:"cache_dir"`
	Config    map[string]string `json:"config"`
	LogLevel  string            `json:"log_level"`
	HostInfo  SDKHostInfo       `json:"host_info"`
}

type SDKHostInfo struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

type SDKInitResult struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Protocol    int    `json:"protocol"`
}

type SDKLoadResult struct {
	SkippedRegistrations []SDKSkippedRegistration `json:"skipped_registrations,omitempty"`
}

type SDKSkippedRegistration struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type SDKHealthResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type SDKMCPCallRequest struct {
	ToolName  string                 `json:"tool_name"`
	Arguments map[string]interface{} `json:"arguments"`
	SessionID string                 `json:"session_id,omitempty"`
}

type SDKMCPCallResult struct {
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error,omitempty"`
}

// ToolNameForOperation creates the plugin-sdk MCP tool name Cerberus uses to
// route a connector operation through a subprocess plugin.
func ToolNameForOperation(connectorID, operation string) string {
	return "cerberus_" + connectorID + "_" + operation
}

func OperationFromToolName(connectorID, toolName string, manifest contract.Manifest) (contract.ManifestOperation, bool) {
	for _, op := range manifest.Operations {
		if toolName == ToolNameForOperation(connectorID, op.Name) {
			return op, true
		}
	}
	return contract.ManifestOperation{}, false
}

func MCPRequestFromOperation(args OperationArgs) SDKMCPCallRequest {
	toolArgs := make(map[string]interface{}, len(args.Config))
	for key, value := range args.Config {
		toolArgs[key] = value
	}
	if args.DryRun {
		toolArgs["dry_run"] = true
	}
	if args.Acknowledged {
		toolArgs["acknowledged"] = true
	}
	return SDKMCPCallRequest{
		ToolName:  ToolNameForOperation(args.Connector, args.Operation),
		Arguments: toolArgs,
	}
}

func OperationResultFromMCP(args OperationArgs, result SDKMCPCallResult) (OperationResult, error) {
	if result.IsError {
		return OperationResult{}, fmt.Errorf("plugin tool %s returned error: %s", ToolNameForOperation(args.Connector, args.Operation), string(result.Content))
	}
	var data any
	if len(result.Content) > 0 {
		if err := json.Unmarshal(result.Content, &data); err != nil {
			data = string(result.Content)
		}
	}
	return OperationResult{
		Connector: args.Connector,
		Operation: args.Operation,
		Data:      data,
	}, nil
}
