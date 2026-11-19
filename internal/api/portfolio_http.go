package api

import (
	"net/http"
	"net/url"

	"github.com/hollis-labs/substrate/apierr"
)

// GET /api/portfolio/health is main's portfolio health projection (cerbapi
// portfolio_health_http.go, the web console's /api/portfolio/health): read scope, recorded as
// main's portfolio.health in supervise mode.

func portfolioQueryFromURL(v url.Values) PortfolioHealthQuery {
	q := PortfolioHealthQuery{
		Mode:         v.Get("mode"),
		Project:      v.Get("project"),
		Host:         v.Get("host"),
		Kind:         v.Get("kind"),
		RollupStatus: v.Get("rollup_status"),
	}
	if raw := v.Get("include_unsupervised"); raw != "" {
		switch raw {
		case "true", "1":
			b := true
			q.IncludeUnsupervised = &b
		case "false", "0":
			b := false
			q.IncludeUnsupervised = &b
		}
	}
	return q
}

func (a *API) portfolioHealth(w http.ResponseWriter, r *http.Request) {
	if a.o.Portfolio == nil {
		apierr.Write(w, http.StatusServiceUnavailable, "portfolio_unavailable", "this Cerberus serves no portfolio health projection", nil)
		return
	}
	ctx := GateContext(r.Context())
	call := &portfolioCall{Portfolio: a.o.Portfolio, gate: a.o.Gate, observer: a.o.Observer,
		views: func() ([]HostView, error) { return a.allViews(r) }}
	out, err := call.Project(ctx, portfolioQueryFromURL(r.URL.Query()))
	if err != nil {
		hostErr(w, err)
		return
	}
	writeScoped(ctx, w, out)
}
