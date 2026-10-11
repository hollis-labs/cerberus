package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// HostHTTP serves the host routes under prefix (no trailing slash), over the
// host client behind it. The socket serves it at /hosts and the web console's
// API at /api/hosts; the host that mounts it runs authentication first.
//
//	GET  {prefix}                     list; ?role= ?provider= ?state= ?label=k=v (repeatable)
//	POST {prefix}/capacity            capacity query (JSON body)
//	GET  {prefix}/{name}              one host
//	POST {prefix}/{name}/probe        measure the host and record the facts
//
// All four are reads. A host named "capacity" is reached with GET.
func HostHTTP(client HostClient, prefix string) http.Handler {
	if client == nil {
		client = unavailableHostClient{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
		parts := []string{}
		if rest != "" {
			parts = strings.Split(rest, "/")
		}
		fail := func(err error) {
			err = scopeError(redact.ScopeFrom(r.Context()), err)
			writeServiceError(w, hostStatus(err), err)
		}
		bad := func(format string) {
			fail(&HostInvalidError{Err: redact.Guidance("%s", format)})
		}
		switch {
		case len(parts) == 0 && r.Method == http.MethodGet:
			filter, err := hostFilterFromQuery(r.URL.Query())
			if err != nil {
				fail(err)
				return
			}
			views, err := client.ListHosts(r.Context(), filter)
			if err != nil {
				fail(err)
				return
			}
			if views == nil {
				views = []HostView{}
			}
			writeJSON(w, http.StatusOK, views)
		case len(parts) == 1 && parts[0] == "capacity" && r.Method == http.MethodPost:
			var q CapacityQuery
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				bad("the capacity request body could not be read")
				return
			}
			if len(strings.TrimSpace(string(body))) > 0 {
				if err = json.Unmarshal(body, &q); err != nil {
					var invalid *HostInvalidError
					if errors.As(err, &invalid) {
						fail(err)
					} else {
						bad("the capacity request body must be a JSON object")
					}
					return
				}
			}
			res, err := client.HostCapacity(r.Context(), q)
			if err != nil {
				fail(err)
				return
			}
			writeJSON(w, http.StatusOK, res)
		case len(parts) == 1 && r.Method == http.MethodGet:
			v, err := client.GetHost(r.Context(), unescape(parts[0]))
			if err != nil {
				fail(err)
				return
			}
			writeJSON(w, http.StatusOK, v)
		case len(parts) == 2 && parts[1] == "probe" && r.Method == http.MethodPost:
			v, err := client.ProbeHost(r.Context(), unescape(parts[0]))
			if err != nil {
				fail(err)
				return
			}
			writeJSON(w, http.StatusOK, v)
		default:
			writeJSONError(w, http.StatusNotFound, "no such host route")
		}
	})
}

func unescape(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

func hostFilterFromQuery(q url.Values) (HostFilter, error) {
	f := HostFilter{Role: q.Get("role"), Provider: q.Get("provider"), State: q.Get("state")}
	for _, kv := range q["label"] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return f, &HostInvalidError{Err: redact.Guidance("a label filter is key=value")}
		}
		if f.Labels == nil {
			f.Labels = map[string]string{}
		}
		f.Labels[k] = v
	}
	return f, nil
}

// hostStatus is the HTTP status for a host error: a missing host is 404, a
// malformed request 400, a gated refusal its connector status, and anything
// else 500.
func hostStatus(err error) int {
	var notFound *HostNotFoundError
	var invalid *HostInvalidError
	switch {
	case errors.As(err, &notFound):
		return http.StatusNotFound
	case errors.As(err, &invalid):
		return http.StatusBadRequest
	}
	return ExternalConnectorHTTPStatus(err, http.StatusInternalServerError)
}

// ---- in-process client ----

// WithHostService binds the host service the client answers host calls from.
func WithHostService(svc *HostService) InProcessOption {
	return func(c *InProcessClient) { c.hosts = svc }
}

func (c *InProcessClient) hostService() (*HostService, error) {
	if c.hosts == nil {
		return nil, errHostsUnavailable
	}
	return c.hosts, nil
}

// ListHosts lists declared hosts.
func (c *InProcessClient) ListHosts(ctx context.Context, f HostFilter) ([]HostView, error) {
	s, err := c.hostService()
	if err != nil {
		return nil, err
	}
	return s.List(ctx, f)
}

// GetHost returns one host.
func (c *InProcessClient) GetHost(ctx context.Context, name string) (HostView, error) {
	s, err := c.hostService()
	if err != nil {
		return HostView{}, err
	}
	return s.Get(ctx, name)
}

// ProbeHost measures one host.
func (c *InProcessClient) ProbeHost(ctx context.Context, name string) (HostView, error) {
	s, err := c.hostService()
	if err != nil {
		return HostView{}, err
	}
	return s.Probe(ctx, name)
}

// HostCapacity answers a capacity query from recorded facts.
func (c *InProcessClient) HostCapacity(ctx context.Context, q CapacityQuery) (CapacityResult, error) {
	s, err := c.hostService()
	if err != nil {
		return CapacityResult{}, err
	}
	return s.Capacity(ctx, q)
}

// ---- socket client ----

// errDaemonPredatesHosts is what a 404 from a daemon that has no host routes
// means. Nothing ran.
var errDaemonPredatesHosts = redact.Guidance("the serving daemon does not have host support; restart it on this build with `cerberus daemon --replace`")

func hostRouteError(err error) error {
	if status, ok := DaemonHTTPStatus(err); ok && status == http.StatusNotFound && strings.Contains(err.Error(), "404 page not found") {
		return errDaemonPredatesHosts
	}
	return err
}

// ListHosts lists declared hosts through the daemon.
func (c *SocketClient) ListHosts(ctx context.Context, f HostFilter) ([]HostView, error) {
	q := url.Values{}
	if f.Role != "" {
		q.Set("role", f.Role)
	}
	if f.Provider != "" {
		q.Set("provider", f.Provider)
	}
	if f.State != "" {
		q.Set("state", f.State)
	}
	for k, v := range f.Labels {
		q.Add("label", k+"="+v)
	}
	path := "/hosts"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []HostView
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, hostRouteError(err)
	}
	return out, nil
}

// GetHost returns one host through the daemon.
func (c *SocketClient) GetHost(ctx context.Context, name string) (HostView, error) {
	var out HostView
	if err := c.doJSON(ctx, http.MethodGet, "/hosts/"+url.PathEscape(name), nil, &out); err != nil {
		return HostView{}, hostRouteError(err)
	}
	return out, nil
}

// ProbeHost measures one host through the daemon.
func (c *SocketClient) ProbeHost(ctx context.Context, name string) (HostView, error) {
	var out HostView
	if err := c.doJSON(ctx, http.MethodPost, "/hosts/"+url.PathEscape(name)+"/probe", struct{}{}, &out); err != nil {
		return HostView{}, hostRouteError(err)
	}
	return out, nil
}

// HostCapacity asks the daemon which hosts satisfy a query.
func (c *SocketClient) HostCapacity(ctx context.Context, q CapacityQuery) (CapacityResult, error) {
	var out CapacityResult
	if err := c.doJSON(ctx, http.MethodPost, "/hosts/capacity", q, &out); err != nil {
		return CapacityResult{}, hostRouteError(err)
	}
	return out, nil
}
