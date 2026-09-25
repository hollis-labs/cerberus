package mcp

import (
	"context"
	"fmt"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// NewCerberusPipelineListTool creates the cerberus_pipeline_list tool.
func NewCerberusPipelineListTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_pipeline_list",
		Description: "List pipelines (budgeted envelope).",
		InputSchema: objectSchema(map[string]interface{}{
			"limit":  limitSchemaProp(),
			"offset": offsetSchemaProp(),
		}),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			list, err := client.ListPipelines(ctx)
			if err != nil {
				return "", err
			}
			if list == nil {
				list = []cerbapi.PipelineInfo{}
			}
			return budgetedList("cerberus_pipeline_list", list, args, "%d pipelines total."), nil
		},
	})
}

// NewCerberusPipelineRunTool creates the cerberus_pipeline_run tool.
//
// Routes through the Client — the daemon's InProcessClient runs the
// pipeline against its live registry; the socket-backed client forwards
// the run to the daemon. Either way the pipeline sees fresh-from-disk
// service definitions because reg.Reload() is called before resolving.
func NewCerberusPipelineRunTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_pipeline_run",
		Description: "Run a pipeline and return per-stage results. Requires acknowledged=true.",
		InputSchema: objectSchema(map[string]interface{}{
			"pipeline_id": map[string]interface{}{
				"type":        "string",
				"description": "Pipeline ID.",
			},
			"acknowledged": map[string]interface{}{
				"type":        "boolean",
				"description": "Acknowledge the run. Required: a stage can run shell commands, so a run is exec.",
			},
		}, "pipeline_id"),
		// A pipeline stage can be a shell action (sh -c), so a run can do anything.
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			pipelineID, _ := args["pipeline_id"].(string)
			if pipelineID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "pipeline_id is required",
				})
			}
			res, err := client.RunPipeline(ctx, pipelineID, cerbapi.WithAcknowledged(boolArg(args, "acknowledged")))
			if err != nil {
				return "", err
			}
			if !res.Success {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   res.Error,
				})
			}
			// Raw already holds the marshaled pipeline.Result JSON; we
			// pass it through verbatim rather than re-indenting.
			if len(res.Raw) > 0 {
				safe, err := redact.JSON(res.Raw)
				return string(safe), err
			}
			return fmt.Sprintf("pipeline %q completed", pipelineID), nil
		},
	})
}
