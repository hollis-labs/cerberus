package mcp

import (
	"context"

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
