package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// cerberus_pipeline_list and cerberus_pipeline_run: main's handlers over /api/pipelines.

func init() {
	handlers["cerberus_pipeline_list"] = pipelineList
	handlers["cerberus_pipeline_run"] = pipelineRun
}

func pipelineList(ctx context.Context, b Backend, args map[string]any) (any, error) {
	var list []supervise.PipelineInfo
	if err := b.Do(ctx, http.MethodGet, "/pipelines", nil, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []supervise.PipelineInfo{}
	}
	return budgetedList("cerberus_pipeline_list", list, args, "%d pipelines total."), nil
}

// pipelineRun returns the executor's result as it came (redacted), as main's did.
func pipelineRun(ctx context.Context, b Backend, args map[string]any) (any, error) {
	id := str(args, "pipeline_id")
	if id == "" {
		return required("pipeline_id")
	}
	ack, _ := args["acknowledged"].(bool)
	body := map[string]any{"acknowledged": ack}
	if a := str(args, "approval_id"); a != "" {
		body["approval_id"] = a
	}
	var res supervise.PipelineRunResult
	if err := b.Do(ctx, http.MethodPost, "/pipelines/"+url.PathEscape(id)+"/run", body, &res); err != nil {
		return nil, err
	}
	if !res.Success {
		return toolResult(lifecycleResult{Success: false, Error: res.Error})
	}
	if len(res.Raw) > 0 {
		safe, err := redact.JSON(res.Raw)
		return string(safe), err
	}
	return fmt.Sprintf("pipeline %q completed", id), nil
}
