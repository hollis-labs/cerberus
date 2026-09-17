package mcp

import (
	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// lifecycleResult preserves the JSON response shape shared by many MCP tools.
type lifecycleResult = cerbapi.OpResult

func marshalResult(r lifecycleResult) string {
	data, _ := redact.MarshalIndent(r, "", "  ")
	return string(data)
}
