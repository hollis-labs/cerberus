package mcp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	cmcp "github.com/hollis-labs/cerberus/internal/mcp"
)

// The pipeline tools are main's over /api/pipelines: the list is budgeted; a run sends the
// acknowledgment and returns the executor's result as it came, or a lifecycle failure.
func TestPipelineTools(t *testing.T) {
	var runBody map[string]any
	fail := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/pipelines":
			_, _ = w.Write([]byte(`[{"id":"ship","name":"Ship","stage_count":2}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/pipelines/ship/run":
			_ = json.NewDecoder(r.Body).Decode(&runBody)
			if fail {
				_, _ = w.Write([]byte(`{"success":false,"error":"resolve pipeline: boom"}`))
				return
			}
			raw, _ := json.Marshal([]byte(`{"pipeline_id":"ship","success":true}`))
			_, _ = w.Write([]byte(`{"success":true,"raw":` + string(raw) + `}`))
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	})
	app := mcpApp(t)
	if err := cmcp.Register(app, cmcp.MuxBackend{Handler: h}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, app, stdioScopes)
	if out, isErr := call(t, cs, "cerberus_pipeline_list", map[string]any{}); isErr || !strings.Contains(out, `"id":"ship"`) || !strings.Contains(out, `"total":1`) {
		t.Fatalf("list: %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "cerberus_pipeline_run", map[string]any{"pipeline_id": "ship", "acknowledged": true, "approval_id": "apr-1"}); isErr ||
		out != `{"pipeline_id":"ship","success":true}` || runBody["acknowledged"] != true || runBody["approval_id"] != "apr-1" {
		t.Fatalf("run: %v %s (body %v)", isErr, out, runBody)
	}
	fail = true
	if out, _ := call(t, cs, "cerberus_pipeline_run", map[string]any{"pipeline_id": "ship", "acknowledged": true}); !strings.Contains(out, "resolve pipeline: boom") {
		t.Fatalf("failed run: %s", out)
	}
	if out, _ := call(t, cs, "cerberus_pipeline_run", map[string]any{}); !strings.Contains(out, "pipeline_id is required") {
		t.Fatalf("no id: %s", out)
	}
}
