package mcp

import (
	"context"
	"math"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

func NewCerberusFleetSnapshotTool(client cerbapi.FleetSnapshotClient) Tool {
	return contractTool(Tool{
		Name:        "cerberus_fleet_snapshot",
		Description: "Fleet health from stored monitor samples, without remote probes: every known host in name order (pages up to 100), roles, nullable reachability/last_seen, latest CPU percent, memory/disk bytes and load1/5/15, plus twelve five-minute last-in-bucket points over the last hour. Null is unmeasured; no interpolation. Main paths include /var/tmp. Follow next_offset until null. No agent or session data.",
		InputSchema: objectSchema(map[string]interface{}{
			"offset": map[string]interface{}{"type": "integer", "minimum": 0},
			"limit":  map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 100},
		}),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			q := cerbapi.FleetSnapshotQuery{}
			for key, dst := range map[string]*int{"offset": &q.Offset, "limit": &q.Limit} {
				if raw, ok := args[key]; ok {
					n, ok := raw.(float64)
					if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < 0 || n > math.MaxInt32 {
						return nil, redact.Guidance("%s must be a nonnegative integer", key)
					}
					*dst = int(n)
				}
			}
			out, err := client.FleetSnapshot(ctx, q)
			if err != nil {
				return nil, err
			}
			b, err := redact.MarshalIndent(out, "", "  ")
			return string(b), err
		},
	})
}

func init() {
	toolOperations["cerberus_"+cerbapi.OpFleetSnapshot] = opRef{cerbapi.HostDefinition, cerbapi.OpFleetSnapshot}
}
