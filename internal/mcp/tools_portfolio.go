package mcp

import (
	"context"
	"net/http"
	"net/url"

	"github.com/hollis-labs/cerberus/internal/api"
)

// cerberus_portfolio_health: main's handler over GET /api/portfolio/health, the projection
// indented as main's.

func init() { handlers["cerberus_portfolio_health"] = portfolioHealthTool }

func portfolioHealthTool(ctx context.Context, b Backend, args map[string]any) (any, error) {
	q := url.Values{}
	for _, k := range []string{"mode", "project", "host", "kind", "rollup_status"} {
		if v := str(args, k); v != "" {
			q.Set(k, v)
		}
	}
	if v, ok := args["include_unsupervised"].(bool); ok {
		if v {
			q.Set("include_unsupervised", "true")
		} else {
			q.Set("include_unsupervised", "false")
		}
	}
	var proj api.PortfolioHealthProjection
	if err := b.Do(ctx, http.MethodGet, withQuery("/portfolio/health", q), nil, &proj); err != nil {
		return nil, err
	}
	return indented(proj)
}
