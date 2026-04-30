package mcp

import (
	"encoding/json"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

// lifecycleResult preserves the JSON response shape shared by many MCP tools.
type lifecycleResult = cerbapi.OpResult

func marshalResult(r lifecycleResult) string {
	data, _ := json.MarshalIndent(r, "", "  ")
	return string(data)
}
