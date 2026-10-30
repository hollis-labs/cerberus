package mcp

import (
	"context"
	"encoding/json"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// ComponentTools are the component reads (Hosts B): spare and installed parts
// recorded in hosts.yaml. Facts only; nothing here suggests where a part goes.
func ComponentTools(client cerbapi.ComponentClient) []Tool {
	return []Tool{
		contractTool(Tool{
			Name:        "cerberus_component_list",
			Description: "List spare and installed parts (RAM, disks, CPUs, other) recorded in hosts.yaml, in id order. A part is installed (host and slot) or spare. Facts only: no suggestion of where a part goes.",
			InputSchema: objectSchema(map[string]interface{}{
				"kind":  map[string]interface{}{"type": "string", "enum": []string{"ram", "disk", "cpu", "other"}},
				"host":  map[string]interface{}{"type": "string", "description": "Only components located on this host."},
				"spare": map[string]interface{}{"type": "boolean", "description": "Only spare components."},
				"limit": limitSchemaProp(), "offset": offsetSchemaProp(),
			}),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				spare, _ := args["spare"].(bool)
				comps, err := client.ListComponents(ctx, cerbapi.ComponentFilter{Kind: stringArg(args, "kind"), Host: stringArg(args, "host"), Spare: spare})
				if err != nil {
					return "", err
				}
				var envelope any
				if err := json.Unmarshal([]byte(budgetedList("cerberus_component_list", comps, args, "%d components total.")), &envelope); err != nil {
					return "", err
				}
				return withNotes(envelope, nil)
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_component_get",
			Description: "Show one component: kind, size, type, speed, serial and where it is (host and slot, or spare).",
			InputSchema: objectSchema(map[string]interface{}{
				"id": map[string]interface{}{"type": "string", "description": "Component id, as in cerberus_component_list."},
			}, "id"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				c, err := client.GetComponent(ctx, stringArg(args, "id"))
				if err != nil {
					return "", err
				}
				data, err := redact.MarshalIndent(c, "", "  ")
				return string(data), err
			},
		}),
	}
}

func init() {
	for _, op := range []string{cerbapi.OpComponentList, cerbapi.OpComponentGet} {
		toolOperations["cerberus_"+op] = opRef{cerbapi.HostDefinition, op}
	}
}
