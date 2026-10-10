package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/scheduling"
)

// ScheduleTools projects the shared DTO with operation fixed by each tool.
func ScheduleTools(service scheduling.Service) []Tool {
	tools := make([]Tool, 0, len(scheduling.Operations))
	for _, operation := range scheduling.Operations {
		op := operation
		jobSchema, err := jsonschema.For[scheduling.Job](nil)
		if err != nil {
			panic(err)
		}
		registrationSchema, schemaErr := jsonschema.For[[]scheduling.Registration](nil)
		if schemaErr != nil {
			panic(schemaErr)
		}
		properties := map[string]interface{}{
			"registration":    registrationSchema,
			"owner_app":       map[string]any{"type": "string", "description": "App selector; confers no permission."},
			"id":              map[string]any{"type": "string"},
			"job":             jobSchema,
			"revision":        map[string]any{"type": "string"},
			"idempotency_key": map[string]any{"type": "string"},
			"request_id":      map[string]any{"type": "string"},
			"state":           map[string]any{"type": "string", "enum": []string{"enabled", "paused", "completed"}},
			"fire_id":         map[string]any{"type": "string"},
			"limit":           map[string]any{"type": "integer"},
			"after":           map[string]any{"type": "string", "description": "RFC3339 dry-run starting instant."},
			"acknowledged":    map[string]any{"type": "boolean", "description": "Acknowledges metadata mutation, never authorizes execution."},
		}
		tool := contractTool(Tool{Name: "cerberus_schedule_" + op, Description: "Schedule " + op + " through the shared service. Engine remains inactive; run_now needs separate exact-fire authority. Per-run capture is available only through a trusted bound shell-only pipeline delivery transport; unsupported or unbound runs report unavailable.", InputSchema: objectSchema(properties), Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			data, err := json.Marshal(args)
			if err != nil {
				return nil, err
			}
			var req scheduling.Call
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&req); err != nil {
				return nil, scheduling.Refusal("invalid", "invalid scheduling arguments")
			}
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				return nil, scheduling.Refusal("invalid", "unexpected scheduling arguments")
			}
			req.Operation = op
			out, err := service.Schedule(ctx, req)
			if err != nil {
				var coded *scheduling.Error
				if !errors.As(err, &coded) {
					coded = &scheduling.Error{Code: scheduling.ErrorCode(err), Message: err.Error()}
				}
				return nil, toolFailure{message: err.Error(), content: map[string]any{"error": coded}}
			}
			return out, nil
		}})
		tools = append(tools, tool)
	}
	return tools
}
func init() {
	for _, op := range scheduling.Operations {
		toolOperations["cerberus_schedule_"+op] = opRef{cerbapi.ScheduleDefinition, op}
	}
}
