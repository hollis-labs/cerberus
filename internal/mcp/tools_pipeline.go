package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

// NewCerberusPipelineListTool creates the cerberus_pipeline_list tool.
func NewCerberusPipelineListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_pipeline_list",
		Description: "Lists all pipelines defined in the Cerberus config.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			list, err := client.ListPipelines(context.Background())
			if err != nil {
				return "", err
			}
			if list == nil {
				list = []cerbapi.PipelineInfo{}
			}
			data, err := json.MarshalIndent(list, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// NewCerberusPipelineRunTool creates the cerberus_pipeline_run tool.
//
// Routes through the Client — the daemon's InProcessClient runs the
// pipeline against its live registry; the socket-backed client forwards
// the run to the daemon. Either way the pipeline sees fresh-from-disk
// service definitions because reg.Reload() is called before resolving.
func NewCerberusPipelineRunTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_pipeline_run",
		Description: "Executes a pipeline by ID. Stages run in dependency order with parallel execution where possible. Returns the full result with per-stage status and duration.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pipeline_id": map[string]interface{}{
					"type":        "string",
					"description": "The pipeline ID to run.",
				},
			},
			"required": []string{"pipeline_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			pipelineID, _ := args["pipeline_id"].(string)
			if pipelineID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "pipeline_id is required",
				}), nil
			}
			res, err := client.RunPipeline(context.Background(), pipelineID)
			if err != nil {
				return "", err
			}
			if !res.Success {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   res.Error,
				}), nil
			}
			// Raw already holds the marshaled pipeline.Result JSON; we
			// pass it through verbatim rather than re-indenting.
			if len(res.Raw) > 0 {
				return string(res.Raw), nil
			}
			return fmt.Sprintf("pipeline %q completed", pipelineID), nil
		},
	}
}
