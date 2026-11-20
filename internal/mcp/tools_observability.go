package mcp

import (
	"context"
	"strings"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// NewCerberusHealthTool creates the cerberus_health tool.
func NewCerberusHealthTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_health",
		Description: "Get daemon and resource health. Optional resource_id filters to one resource.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional resource ID filter.",
			},
		}),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			filterID, _ := args["resource_id"].(string)
			h, err := client.Health(ctx, filterID)
			if err != nil {
				return "", err
			}
			data, err := redact.MarshalIndent(h, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	})
}

// NewCerberusPortfolioHealthTool creates the cerberus_portfolio_health tool.
func NewCerberusPortfolioHealthTool(client cerbapi.Client) Tool {
	pClient := cerbapi.PortfolioHealthClientOf(client)
	return contractTool(Tool{
		Name:        "cerberus_portfolio_health",
		Description: "Get combined portfolio health projection (cerberus.portfolio.health.v1) across hosts, projects, deployments, shared containers, and connectors.",
		InputSchema: objectSchema(map[string]interface{}{
			"mode": map[string]interface{}{
				"type":        "string",
				"enum":        []string{cerbapi.PortfolioModeCached, cerbapi.PortfolioModeLiveBounded},
				"description": "Observation mode: cached (default) or live_bounded.",
			},
			"project": map[string]interface{}{
				"type":        "string",
				"description": "Optional project ID filter.",
			},
			"host": map[string]interface{}{
				"type":        "string",
				"description": "Optional host name filter.",
			},
			"kind": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"local_service", "remote_service", "shared_infra", "connector_handle"},
				"description": "Optional deployment kind filter.",
			},
			"rollup_status": map[string]interface{}{
				"type":        "string",
				"enum":        []string{cerbapi.RollupHealthy, cerbapi.RollupDegraded, cerbapi.RollupDown, cerbapi.RollupUnknown, cerbapi.RollupExpectedStopped, cerbapi.RollupUnsupervised, cerbapi.RollupStale},
				"description": "Optional rollup status filter.",
			},
			"include_unsupervised": map[string]interface{}{
				"type":        "boolean",
				"description": "Include unsupervised connector handles (default true).",
			},
		}),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			var q cerbapi.PortfolioHealthQuery
			q.Mode, _ = args["mode"].(string)
			q.Project, _ = args["project"].(string)
			q.Host, _ = args["host"].(string)
			q.Kind, _ = args["kind"].(string)
			q.RollupStatus, _ = args["rollup_status"].(string)
			if v, ok := args["include_unsupervised"].(bool); ok {
				q.IncludeUnsupervised = &v
			}
			proj, err := pClient.PortfolioHealth(ctx, q)
			if err != nil {
				return "", err
			}
			data, err := redact.MarshalIndent(proj, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	})
}

// NewCerberusFleetInventoryTool creates the cerberus_fleet_inventory tool.
func NewCerberusFleetInventoryTool(client cerbapi.Client) Tool {
	fClient := cerbapi.FleetClientOf(client)
	return contractTool(Tool{
		Name: "cerberus_fleet_inventory",
		Description: "What runs on each fleet host and what it was built from. " +
			"Reports (cerberus.fleet.inventory.v1) every service, container and installed binary " +
			"with its VCS revision, whether the build came from a dirty tree, whether a process still runs a binary since replaced on disk (pending restart), " +
			"how many commits it is behind its repository's default branch with the merged PRs it lacks, and executables that differ across hosts (drift). " +
			"Use it to answer 'is PR #N live?' or 'what version of X runs where?'. Results are cached for a minute; refresh=true probes again.",
		InputSchema: objectSchema(map[string]interface{}{
			"hosts":   map[string]interface{}{"type": "string", "description": "Comma-separated host names; empty is every host."},
			"role":    map[string]interface{}{"type": "string", "description": "Only hosts with this role (hub, worker, builder, ...)."},
			"refresh": map[string]interface{}{"type": "boolean", "description": "Bypass the one-minute cache."},
		}),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			var q cerbapi.FleetInventoryQuery
			if hs, _ := args["hosts"].(string); strings.TrimSpace(hs) != "" {
				for _, h := range strings.Split(hs, ",") {
					if h = strings.TrimSpace(h); h != "" {
						q.Hosts = append(q.Hosts, h)
					}
				}
			}
			q.Role, _ = args["role"].(string)
			q.Refresh, _ = args["refresh"].(bool)
			inv, err := fClient.FleetInventory(ctx, q)
			if err != nil {
				return "", err
			}
			data, err := redact.MarshalIndent(inv, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	})
}
