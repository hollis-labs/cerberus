package mcp

import (
	"fmt"
	"log"

	"github.com/hollis-labs/go-mcp/budget"
)

// defaultListLimit caps MCP list responses. It uses the budget package's
// MaxLimit (25) to bound responses while offset paging keeps larger lists
// recoverable. The tuning review (CW-20260909-0009) retained this default:
// paging handles the inventory without raising the shared library's cap.
// Truncations remain logged as mcp.budget.truncated for future tuning.
const defaultListLimit = budget.MaxLimit

// limitSchemaProp is the shared "limit" input-schema property for budgeted
// list tools.
func limitSchemaProp() map[string]interface{} {
	return map[string]interface{}{
		"type":        "integer",
		"description": "Max items to return (1-25, default 25). Combine with offset to page through a truncated list.",
	}
}

// offsetSchemaProp is the shared "offset" input-schema property for budgeted
// list tools, so truncated lists stay fully recoverable via paging.
func offsetSchemaProp() map[string]interface{} {
	return map[string]interface{}{
		"type":        "integer",
		"description": "Number of items to skip before this page (default 0). Use with limit to page through results.",
	}
}

// budgetedList applies the response budget to a list tool result. It honors
// optional "limit" (1..budget.MaxLimit) and "offset" args, returning a
// budgeted envelope ({items,count,total,truncated,hint}) for the requested
// page. When the response doesn't contain the whole list it sets Truncated and
// a hint (the tool-supplied template, which receives the total, plus paging
// guidance), and logs the truncation for tuning review. hintTemplate is a
// fmt template taking the total count as its single %d.
func budgetedList[T any](tool string, items []T, args map[string]interface{}, hintTemplate string) string {
	if items == nil {
		items = []T{} // marshal as [] not null, regardless of paging
	}
	limit := budget.ExtractLimit(args, defaultListLimit)
	_, offset := budget.ExtractPagination(args)

	total := len(items)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := items[offset:end]

	env := budget.Envelope{
		Items:     page,
		Count:     len(page),
		Total:     total,
		Truncated: len(page) < total,
	}
	if env.Truncated {
		env.Hint = fmt.Sprintf(hintTemplate, total)
		if env.Count > 0 {
			env.Hint += fmt.Sprintf(" Showing %d-%d of %d.", offset+1, end, total)
		}
		env.Hint += " Pass offset/limit (limit max 25) to page through the rest."
		// Logged to stderr (safe alongside the stdio MCP protocol on stdout)
		// for tuning reviews: grep `mcp.budget.truncated`.
		log.Printf("mcp.budget.truncated tool=%s total=%d returned=%d offset=%d limit=%d", tool, total, env.Count, offset, limit)
	}
	return budget.ToolJSON(env)
}
