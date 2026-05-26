package mcp

import (
	"log"

	"github.com/hollis-labs/go-mcp/budget"
)

// defaultListLimit caps MCP list responses. It uses the budget package's
// MaxLimit (25): generous enough that current operational lists rarely
// truncate, while bounding pathological responses well under the ~25k-token
// MCP tool-response ceiling (MCPContentTooLargeError). Truncations are logged
// (mcp.budget.truncated) so the limit can be tuned from real data — see the
// scheduled 2026-06-01 review.
const defaultListLimit = budget.MaxLimit

// limitSchemaProp is the shared "limit" input-schema property for budgeted
// list tools.
func limitSchemaProp() map[string]interface{} {
	return map[string]interface{}{
		"type":        "integer",
		"description": "Max items to return (1-25, default 25). If the response is truncated, narrow with the other filters.",
	}
}

// budgetedList applies the response budget to a list tool result: it honors an
// optional "limit" arg (1..budget.MaxLimit), truncates with a
// progressive-disclosure hint, logs any truncation for tuning review, and
// returns the JSON envelope ({items,count,total,truncated,hint}).
func budgetedList[T any](tool string, items []T, args map[string]interface{}, hint string) string {
	limit := budget.ExtractLimit(args, defaultListLimit)
	env := budget.Apply(items, budget.Config{Limit: limit}, hint)
	if env.Truncated {
		// Logged to stderr (safe alongside the stdio MCP protocol on stdout)
		// for the 1-week tuning review: grep `mcp.budget.truncated`.
		log.Printf("mcp.budget.truncated tool=%s total=%d returned=%d limit=%d", tool, env.Total, env.Count, limit)
	}
	return budget.ToolJSON(env)
}
