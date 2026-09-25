package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBudgetedList(t *testing.T) {
	items := make([]int, 30)
	for i := range items {
		items[i] = i
	}

	type envelope struct {
		Items     []int `json:"items"`
		Count     int   `json:"count"`
		Total     int   `json:"total"`
		Truncated bool  `json:"truncated"`
		Hint      string
	}
	decode := func(t *testing.T, s string) envelope {
		t.Helper()
		var e envelope
		if err := json.Unmarshal([]byte(s), &e); err != nil {
			t.Fatalf("unmarshal envelope: %v (%s)", err, s)
		}
		return e
	}

	// Default (no args): capped at MaxLimit (25), truncated, hint set.
	e := decode(t, budgetedList("test_tool", items, nil, "%d items"))
	if e.Total != 30 || e.Count != defaultListLimit || !e.Truncated {
		t.Fatalf("default: %+v (want total=30 count=%d truncated)", e, defaultListLimit)
	}
	if e.Hint == "" {
		t.Fatal("expected a hint when truncated")
	}

	// Explicit limit, passed as float64 to match JSON-decoded MCP args.
	e = decode(t, budgetedList("test_tool", items, map[string]interface{}{"limit": float64(5)}, "%d items"))
	if e.Count != 5 || e.Total != 30 || !e.Truncated || e.Items[0] != 0 {
		t.Fatalf("limit=5: %+v", e)
	}

	// Offset paging (float64 args): skip 25 -> the final 5 of 30.
	e = decode(t, budgetedList("test_tool", items, map[string]interface{}{"offset": float64(25)}, "%d items"))
	if e.Count != 5 || e.Total != 30 || !e.Truncated || e.Items[0] != 25 {
		t.Fatalf("offset=25: %+v (want last page starting at 25)", e)
	}

	// Under the limit: full list, not truncated, no hint.
	e = decode(t, budgetedList("test_tool", items[:3], nil, "%d items"))
	if e.Truncated || e.Count != 3 {
		t.Fatalf("under-limit: %+v", e)
	}
	if e.Hint != "" {
		t.Fatalf("unexpected hint when not truncated: %q", e.Hint)
	}

	// Nil slice encodes as [] (not null) and is not truncated.
	out := budgetedList("test_tool", []int(nil), nil, "%d items")
	if !strings.Contains(out, `"items":[]`) {
		t.Fatalf("nil slice should encode items as []: %s", out)
	}
}

// A budgeted list goes through the regex net: on the daemon's stdio server
// its items come from InProcessClient, which no server has redacted.
func TestBudgetedListIsRedacted(t *testing.T) {
	out := budgetedList("t", []map[string]string{{"name": "svc", "api_token": "abcdef1234567890"}}, nil, "%d")
	if strings.Contains(out, "abcdef1234567890") || !strings.Contains(out, `"name":"svc"`) {
		t.Fatalf("budgetedList = %s", out)
	}
}
