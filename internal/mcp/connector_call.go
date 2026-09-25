package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// The shared path from an MCP tool to a connector operation. Every connector's
// tools use it, so it belongs to none of them: a connector moving out to a
// plugin must be able to take its tools file with it.

func executeConnectorMCP(ctx context.Context, client cerbapi.Client, connectorID, operation string, cfg map[string]any, dryRun, acknowledged bool) (any, error) {
	result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
		Connector:    connectorID,
		Operation:    operation,
		Config:       cfg,
		DryRun:       dryRun,
		Acknowledged: acknowledged,
	})
	if err != nil {
		return connectorFailure(ctx, err)
	}
	return marshalConnectorData(result.Data)
}

// connectorFailure is how a tool relays a failed call: a tool error whose
// body is the OpResult shape. The text is rendered once, here, in the call's
// scope — a pre-rendered error keeps its prose, anything else gets the rules
// — and marked rendered, so the edges after this (the failure's content, the
// scoped tool error) only remove values from it.
func connectorFailure(ctx context.Context, err error) (any, error) {
	scope := redact.ScopeFrom(ctx)
	msg := scope.ErrorText(err)
	scope.MarkRendered(msg)
	return nil, toolFailure{message: msg, content: lifecycleResult{Success: false, Error: msg}, scope: scope}
}

func marshalConnectorData(data any) (string, error) {
	if text, ok := data.(string); ok {
		return redact.Text(text), nil
	}
	if data == nil {
		return marshalResult(lifecycleResult{Success: true}), nil
	}
	out, err := redact.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func boolArg(args map[string]interface{}, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func stringArg(args map[string]interface{}, key string) string {
	value, _ := args[key].(string)
	return value
}
