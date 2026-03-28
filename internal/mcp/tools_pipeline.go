package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/pipeline"
	"github.com/chrispian/cerberus/internal/service"
)

// pipelineListEntry is the JSON output for a single pipeline.
type pipelineListEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Stages      int    `json:"stage_count"`
}

// NewCerberusPipelineListTool creates the cerberus_pipeline_list tool.
func NewCerberusPipelineListTool(cfg *config.ConfigV2) Tool {
	return Tool{
		Name:        "cerberus_pipeline_list",
		Description: "Lists all pipelines defined in the Cerberus config.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			entries := make([]pipelineListEntry, 0, len(cfg.Pipelines))
			for _, p := range cfg.Pipelines {
				entries = append(entries, pipelineListEntry{
					ID:          p.ID,
					Name:        p.Name,
					Description: p.Description,
					Stages:      len(p.Stages),
				})
			}
			data, err := json.MarshalIndent(entries, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// NewCerberusPipelineRunTool creates the cerberus_pipeline_run tool.
func NewCerberusPipelineRunTool(cfg *config.ConfigV2, services []*service.ManagedService, local *localconn.Connector) Tool {
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

			// Find pipeline definition
			var pdef *config.PipelineDef
			for i := range cfg.Pipelines {
				if cfg.Pipelines[i].ID == pipelineID {
					pdef = &cfg.Pipelines[i]
					break
				}
			}
			if pdef == nil {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   fmt.Sprintf("pipeline %q not found in config", pipelineID),
				}), nil
			}

			// Resolve and execute
			p, err := pipeline.Resolve(*pdef, services, local)
			if err != nil {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   fmt.Sprintf("resolve pipeline: %s", err.Error()),
				}), nil
			}

			env := &domain.PipelineEnv{Values: make(map[string]any)}
			exec := pipeline.NewExecutor(nil)
			result, err := exec.Run(context.Background(), p, env)
			if err != nil {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   fmt.Sprintf("pipeline execution: %s", err.Error()),
				}), nil
			}

			data, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}
