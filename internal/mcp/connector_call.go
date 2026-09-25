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
		return toolResult(lifecycleResult{Success: false, Error: err.Error()})
	}
	return marshalConnectorData(result.Data)
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
