package mcp

import (
	"context"
	"fmt"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/pkg/outpost"
)

func HostActionTools(client cerbapi.HostClient) []Tool {
	var tools []Tool
	for _, verb := range []string{"units", "logs", "start", "stop", "restart", "plan"} {
		props := map[string]interface{}{
			"name":         map[string]interface{}{"type": "string", "description": "Declared host name."},
			"unit":         map[string]interface{}{"type": "string", "description": "Native user-service unit identifier."},
			"lines":        map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 1000, "description": "Log line limit; logs only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "The operator asked for this operation."},
			"approval_id":  map[string]interface{}{"type": "string", "description": "Out-of-band approval bound to this exact plan."},
		}
		required := []string{"name"}
		if verb == "plan" {
			delete(props, "approval_id")
			delete(props, "acknowledged")
			props["verb"] = map[string]interface{}{"type": "string", "enum": []string{"units", "logs", "start", "stop", "restart"}}
			required = append(required, "verb")
		} else if verb != "units" {
			required = append(required, "unit")
		}
		tools = append(tools, contractTool(Tool{Name: "cerberus_host_" + verb,
			Description: "Run the declared host's scoped Outpost " + verb + " operation through Cerberus policy. Mutations require the controller's approval flow; plan performs no remote operation.",
			InputSchema: objectSchema(props, required...), Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				c, ok := client.(cerbapi.HostActionClient)
				if !ok {
					return nil, fmt.Errorf("serving daemon has no host action support")
				}
				operation := verb
				if verb == "plan" {
					operation = stringArg(args, "verb")
				}
				r := outpost.Request{Verb: operation, Unit: stringArg(args, "unit")}
				if n, ok := args["lines"].(float64); ok {
					r.Lines = int(n)
				}
				a := cerbapi.HostActionArgs{Request: r, Acknowledged: boolArg(args, "acknowledged"), ApprovalID: stringArg(args, "approval_id")}
				if verb == "plan" {
					return c.PlanHostAction(ctx, stringArg(args, "name"), a)
				}
				return c.ExecuteHostAction(ctx, stringArg(args, "name"), a)
			}}))
	}
	return tools
}

func init() {
	for _, verb := range []string{"units", "logs", "start", "stop", "restart", "plan"} {
		toolOperations["cerberus_host_"+verb] = opRef{cerbapi.HostActionDefinition, verb}
	}
}
