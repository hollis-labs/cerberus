package pluginhost

import (
	"encoding/json"
	"fmt"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	plugin "github.com/hollis-labs/cerberus/pkg/plugin"
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

	// Granted names the capabilities the host allowed, so a plugin can degrade
	// instead of assuming it received what it asked for. Omitted when empty,
	// which keeps the payload byte-identical to what a pre-capability host
	// sent.
	Granted []string `json:"granted,omitempty"`
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
// route a connector operation through a subprocess plugin. Naming is part of
// the public authoring contract — a plugin serves the names the host routes
// against — so it lives in pkg/plugin, reachable from an external plugin
// module. The SDK* protocol types above stay host-side.
func ToolNameForOperation(connectorID, operation string) string {
	return plugin.ToolNameForOperation(connectorID, operation)
}

// OperationFromToolName resolves an MCP tool name back to the manifest
// operation it was derived from.
func OperationFromToolName(connectorID, toolName string, manifest contract.Manifest) (contract.ManifestOperation, bool) {
	return plugin.OperationFromToolName(connectorID, toolName, manifest)
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
		if code, message, ok := plugin.ParseErrorResult(result.Content); ok {
			if !code.Valid() {
				// A code outside the plugin vocabulary is reported, not
				// trusted: it cannot pose as one of the gate's own codes.
				message = fmt.Sprintf("%s (the plugin sent unknown error code %q)", message, code)
				code = plugin.ErrorOperationFailed
			}
			return OperationResult{}, &CodedError{Connector: args.Connector, Operation: args.Operation, Code: code, Message: message}
		}
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
