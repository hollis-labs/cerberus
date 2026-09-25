package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hollis-labs/go-mcp/budget"
	gmcp "github.com/hollis-labs/go-mcp/server"

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
//
// Whatever the tool returns is rendered through that scope on the way out —
// its result, its error and the notifications it sends while it runs —
// because go-mcp writes all three with encoding/json or err.Error(), and a
// tool on InProcessClient hands back text no server has redacted.
func withRequestScope(tool Tool) Tool {
	handler := tool.Handler
	tool.Handler = func(ctx context.Context, args map[string]any) (any, error) {
		parent := ctx
		ctx, scope := redact.EnsureScope(ctx)
		ctx = gmcp.WithNotifier(ctx, func(n gmcp.Notification) {
			gmcp.Notify(parent, scopedNotification(scope, n))
		})
		result, err := handler(ctx, args)
		if err != nil {
			return nil, scopedToolError(scope, err)
		}
		return scopedToolResult(scope, result), nil
	}
	return tool
}

// scopedToolResult renders a result through the scope. A string is already
// encoded — usually JSON the tool marshaled through the regex net — so it
// only loses the scope's values: a rule run over an encoded document can
// break it. Anything else is marshaled here, through the scope and the net.
func scopedToolResult(scope *redact.Scope, result any) any {
	switch v := result.(type) {
	case nil:
		return nil
	case string:
		return scope.ReplaceValues(v)
	default:
		data, err := scope.Marshal(v)
		if err != nil {
			return result
		}
		return json.RawMessage(data)
	}
}

// scopedToolError renders err through the scope in the shape go-mcp reads:
// a *budget.ToolError keeps its fields, a structured error keeps its content,
// and any other error becomes its redacted text.
func scopedToolError(scope *redact.Scope, err error) error {
	var toolErr *budget.ToolError
	if errors.As(err, &toolErr) {
		redacted := *toolErr
		redacted.Message = scope.Text(toolErr.Message)
		redacted.NextStep = scope.Text(toolErr.NextStep)
		return &redacted
	}
	var structured budget.StructuredError
	if errors.As(err, &structured) {
		return scopedStructuredError{source: structured, scope: scope}
	}
	return errors.New(scope.ErrorText(err))
}

// scopedStructuredError deliberately has no Unwrap: go-mcp looks for a
// *budget.ToolError before a structured error, and must find neither behind
// this one.
type scopedStructuredError struct {
	source budget.StructuredError
	scope  *redact.Scope
}

func (e scopedStructuredError) Error() string { return e.scope.Text(e.source.Error()) }
func (e scopedStructuredError) ToolErrorContent() any {
	data, err := e.scope.Marshal(e.source.ToolErrorContent())
	if err != nil {
		data, _ = json.Marshal(map[string]any{"success": false, "error": e.Error()})
	}
	return json.RawMessage(data)
}

// scopedNotification renders a notification's params through the scope: a
// progress or log message a tool sends mid-call, such as a failed pipeline
// stage's output. The params go back as a map, the shape go-mcp's notifier
// reads to build the protocol message; a number keeps its value.
func scopedNotification(scope *redact.Scope, n gmcp.Notification) gmcp.Notification {
	if n.Params == nil {
		return n
	}
	data, err := scope.Marshal(n.Params)
	if err != nil {
		return n
	}
	var params map[string]any
	if json.Unmarshal(data, &params) == nil {
		n.Params = params
	}
	return n
}
