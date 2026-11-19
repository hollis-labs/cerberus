package mcp_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	cmcp "github.com/hollis-labs/cerberus/internal/mcp"
)

// cerberus_portfolio_health sends main's arguments as the query and answers with the projection
// indented, as main's.
func TestPortfolioHealthTool(t *testing.T) {
	var query url.Values
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/portfolio/health" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"schema_version":1,"contract":"cerberus.portfolio.health.v1","summary":{"overall_status":"degraded"}}`))
	})
	app := mcpApp(t)
	if err := cmcp.Register(app, cmcp.MuxBackend{Handler: h}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, app, stdioScopes)
	out, isErr := call(t, cs, "cerberus_portfolio_health", map[string]any{"mode": "live_bounded", "project": "shop", "include_unsupervised": false})
	if isErr || query.Get("mode") != "live_bounded" || query.Get("project") != "shop" || query.Get("include_unsupervised") != "false" {
		t.Fatalf("portfolio: %v %s (query %v)", isErr, out, query)
	}
	if !strings.Contains(out, `"contract": "cerberus.portfolio.health.v1"`) || !strings.Contains(out, `"overall_status": "degraded"`) {
		t.Fatalf("portfolio output: %s", out)
	}
}
