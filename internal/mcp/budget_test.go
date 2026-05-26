package mcp

import (
	"encoding/json"
	"testing"
)

func TestBudgetedList(t *testing.T) {
	items := make([]int, 30)
	for i := range items {
		items[i] = i
	}

	type envelope struct {
		Count     int  `json:"count"`
		Total     int  `json:"total"`
		Truncated bool `json:"truncated"`
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

	// Default (no limit arg): capped at MaxLimit (25), truncated, hint set.
	e := decode(t, budgetedList("test_tool", items, nil, "%d items"))
	if e.Total != 30 || e.Count != defaultListLimit || !e.Truncated {
		t.Fatalf("default: %+v (want total=30 count=%d truncated)", e, defaultListLimit)
	}
	if e.Hint == "" {
		t.Fatal("expected a hint when truncated")
	}

	// Explicit limit honored.
	e = decode(t, budgetedList("test_tool", items, map[string]interface{}{"limit": 5}, "%d items"))
	if e.Count != 5 || e.Total != 30 || !e.Truncated {
		t.Fatalf("limit=5: %+v", e)
	}

	// Under the limit: full list, not truncated, no hint.
	e = decode(t, budgetedList("test_tool", items[:3], nil, "%d items"))
	if e.Truncated || e.Count != 3 {
		t.Fatalf("under-limit: %+v", e)
	}
	if e.Hint != "" {
		t.Fatalf("unexpected hint when not truncated: %q", e.Hint)
	}
}
