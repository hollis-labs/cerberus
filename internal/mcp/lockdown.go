package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// brakeEngager is the daemon's brakes, as the lockdown tool uses them.
type brakeEngager interface {
	EngageBrake(ctx context.Context, args cerbapi.BrakeEngageArgs) (cerbapi.BrakesView, error)
}

// NewCerberusLockdownTool creates cerberus_lockdown: engage the lockdown
// (§12), and nothing else. An agent that sees itself going wrong can stop
// itself; engaging is self-restriction and harmless. There is no tool that
// lifts it: a person does, on a terminal.
func NewCerberusLockdownTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name: "cerberus_lockdown",
		Description: "Put Cerberus in lockdown: every operation but a plain read is refused until a person lifts it. Use it when you believe you, or " +
			"something acting through Cerberus, is doing harm, and say why. You cannot lift it; a person does, with `cerberus lockdown --off`.",
		InputSchema: objectSchema(map[string]interface{}{
			"reason": map[string]interface{}{"type": "string", "description": "Why, for the operator and the record."},
		}, "reason"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			engager, ok := client.(brakeEngager)
			if !ok {
				return toolResult(lifecycleResult{Success: false, Error: "this MCP server cannot reach the brakes; it is not connected to the Cerberus daemon"})
			}
			view, err := engager.EngageBrake(ctx, cerbapi.BrakeEngageArgs{Reason: stringArg(args, "reason")})
			if err != nil {
				return connectorFailure(ctx, err)
			}
			return lockdownResult{Success: true, Brakes: view.State,
				NextStep: "Cerberus is in lockdown: only plain reads run. Tell your operator why; they lift it with `cerberus lockdown --off`."}, nil
		},
	})
}

type lockdownResult struct {
	Success  bool        `json:"success"`
	Brakes   brake.State `json:"brakes"`
	NextStep string      `json:"next_step"`
}
