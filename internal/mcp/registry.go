package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// AllTools is every Cerberus MCP tool, in registration order. `cerberus mcp`,
// mcp-http and the daemon's stdio server all serve exactly this list, and
// conformance enumerates it, so a tool cannot be served without its hints
// being checked against its operation's contract.
func AllTools(client cerbapi.Client) []Tool {
	tools := []Tool{
		NewCerberusHealthTool(client),
		NewCerberusProjectListTool(client),
		NewCerberusResourceListTool(client),
		NewCerberusResourceStatusTool(client),
		NewCerberusResourceInspectTool(client),
		NewCerberusResourceDoctorTool(client),
		NewCerberusResourceLogsTool(client),
		NewCerberusResourceReloadTool(client),
		NewCerberusResourceStopTool(client),
		NewCerberusResourceDeployTool(client),
		NewCerberusResourceEnsureFreshTool(client),
		NewCerberusResourceSyncTool(client),
		NewCerberusResourceApplyTool(client),
		NewCerberusResourceRemoveTool(client),
		NewCerberusPipelineListTool(client),
		NewCerberusPipelineRunTool(client),
		NewCerberusConnectorListTool(client),
		NewCerberusConnectorDescribeTool(client),
		NewCerberusGithubStatusTool(client),
		NewCerberusGithubReleasesTool(client),
		NewCerberusGithubRunsTool(client),
		NewCerberusSSHExecTool(client),
		NewCerberusSSHStatusTool(client),
		NewCerberusSSHPutTool(client),
		NewCerberusSSHGetTool(client),
		NewCerberusSSHPutDirTool(client),
		NewCerberusSSHGetDirTool(client),
		NewCerberusDockerPSTool(client),
		NewCerberusDockerLogsTool(client),
		NewCerberusDockerUpTool(client),
		NewCerberusDockerDownTool(client),
		NewCerberusDockerDestroyTool(client),
	}
	for i := range tools {
		tools[i] = withRequestScope(tools[i])
	}
	return tools
}

// withRequestScope gives each tool call its own redaction scope, since an MCP
// server has no middleware chain to create one. The daemon's stdio server
// runs its tools in-process, where the call's credentials resolve, so this
// is the only scope they get; behind `cerberus mcp` and mcp-http the call
// crosses the socket, whose server begins a request of its own, and this
// scope stays empty and costs nothing. It does not mark a surface: MCP
// served in-process is left unmarked, and so treated as remote, exactly as
// before.
func withRequestScope(tool Tool) Tool {
	handler := tool.Handler
	tool.Handler = func(ctx context.Context, args map[string]any) (any, error) {
		ctx, _ = redact.EnsureScope(ctx)
		return handler(ctx, args)
	}
	return tool
}
