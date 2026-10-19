package cerbapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// PortfolioHealthHTTP serves the read-only portfolio health projection over
// HTTP. Mounted on the daemon unix socket at /portfolio/health and
// /v1/portfolio/health, and on the web console at /api/portfolio/health.
func PortfolioHealthHTTP(client PortfolioHealthClient) http.Handler {
	if client == nil {
		client = unavailablePortfolioHealthClient{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		q := portfolioQueryFromURL(r.URL.Query())
		proj, err := client.PortfolioHealth(r.Context(), q)
		if err != nil {
			err = scopeError(redact.ScopeFrom(r.Context()), err)
			writeServiceError(w, portfolioStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, proj)
	})
}

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

func portfolioStatus(err error) int {
	var invalid *PortfolioInvalidError
	if errors.As(err, &invalid) {
		return http.StatusBadRequest
	}
	return ExternalConnectorHTTPStatus(err, http.StatusInternalServerError)
}

// WithPortfolioHealthService injects an explicit PortfolioHealthService into an
// InProcessClient.
func WithPortfolioHealthService(svc *PortfolioHealthService) InProcessOption {
	return func(c *InProcessClient) {
		c.portfolio = svc
	}
}

// WithPortfolioHealthOptions tunes the PortfolioHealthService lazily built by
// an InProcessClient.
func WithPortfolioHealthOptions(opts PortfolioHealthOptions) InProcessOption {
	return func(c *InProcessClient) {
		c.portfolioOpts = opts
	}
}

func (c *InProcessClient) portfolioService() *PortfolioHealthService {
	c.portfolioMu.Lock()
	defer c.portfolioMu.Unlock()
	if c.portfolio != nil {
		return c.portfolio
	}
	sink := c.audit
	if sink == nil && c.runtime != nil && c.runtime.audit != nil {
		sink = c.runtime.audit
	}
	if sink == nil && c.hosts != nil && c.hosts.audit != nil {
		sink = c.hosts.audit
	}
	c.portfolio = NewPortfolioHealthService(
		sink,
		c.runtime,
		c.hosts,
		c.external,
		c.managedPlugins,
		c.portfolioOpts,
	)
	return c.portfolio
}

// PortfolioHealth implements PortfolioHealthClient on InProcessClient.
func (c *InProcessClient) PortfolioHealth(ctx context.Context, q PortfolioHealthQuery) (*PortfolioHealthProjection, error) {
	return c.portfolioService().Project(ctx, q)
}

// PortfolioHealth implements PortfolioHealthClient on SocketClient.
func (c *SocketClient) PortfolioHealth(ctx context.Context, q PortfolioHealthQuery) (*PortfolioHealthProjection, error) {
	vals := url.Values{}
	if q.Mode != "" {
		vals.Set("mode", q.Mode)
	}
	if q.Project != "" {
		vals.Set("project", q.Project)
	}
	if q.Host != "" {
		vals.Set("host", q.Host)
	}
	if q.Kind != "" {
		vals.Set("kind", q.Kind)
	}
	if q.RollupStatus != "" {
		vals.Set("rollup_status", q.RollupStatus)
	}
	if q.IncludeUnsupervised != nil {
		if *q.IncludeUnsupervised {
			vals.Set("include_unsupervised", "true")
		} else {
			vals.Set("include_unsupervised", "false")
		}
	}
	path := "/v1/portfolio/health"
	if enc := vals.Encode(); enc != "" {
		path += "?" + enc
	}
	var out PortfolioHealthProjection
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
