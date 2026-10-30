package cerbapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// ComponentHTTP serves the component reads under prefix (no trailing slash):
// the socket serves it at /components and the web console's API at
// /api/components.
//
//	GET {prefix}        list; ?kind= ?host= ?spare=true
//	GET {prefix}/{id}   one component
func ComponentHTTP(client ComponentClient, prefix string) http.Handler {
	if client == nil {
		client = unavailableHostClient{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
		fail := func(err error) {
			err = scopeError(redact.ScopeFrom(r.Context()), err)
			writeServiceError(w, componentStatus(err), err)
		}
		if componentWriteHTTP(w, r, client, rest) {
			return
		}
		switch {
		case r.Method != http.MethodGet:
			writeJSONError(w, http.StatusNotFound, "no such component route")
		case rest == "":
			q := r.URL.Query()
			f := ComponentFilter{Kind: q.Get("kind"), Host: q.Get("host"), Spare: q.Get("spare") == "true"}
			out, err := client.ListComponents(r.Context(), f)
			if err != nil {
				fail(err)
				return
			}
			if out == nil {
				out = []hosts.Component{}
			}
			writeJSON(w, http.StatusOK, out)
		case !strings.Contains(rest, "/"):
			c, err := client.GetComponent(r.Context(), unescape(rest))
			if err != nil {
				fail(err)
				return
			}
			writeJSON(w, http.StatusOK, c)
		default:
			writeJSONError(w, http.StatusNotFound, "no such component route")
		}
	})
}

func componentStatus(err error) int { return hostStatus(err) }

// ListComponents lists declared components.
func (c *InProcessClient) ListComponents(ctx context.Context, f ComponentFilter) ([]hosts.Component, error) {
	s, err := c.hostService()
	if err != nil {
		return nil, err
	}
	return s.ListComponents(ctx, f)
}

// GetComponent returns one component.
func (c *InProcessClient) GetComponent(ctx context.Context, id string) (hosts.Component, error) {
	s, err := c.hostService()
	if err != nil {
		return hosts.Component{}, err
	}
	return s.GetComponent(ctx, id)
}

// ListComponents lists declared components through the daemon.
func (c *SocketClient) ListComponents(ctx context.Context, f ComponentFilter) ([]hosts.Component, error) {
	q := url.Values{}
	if f.Kind != "" {
		q.Set("kind", f.Kind)
	}
	if f.Host != "" {
		q.Set("host", f.Host)
	}
	if f.Spare {
		q.Set("spare", "true")
	}
	path := "/components"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []hosts.Component
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, hostRouteError(err)
	}
	return out, nil
}

// GetComponent returns one component through the daemon.
func (c *SocketClient) GetComponent(ctx context.Context, id string) (hosts.Component, error) {
	var out hosts.Component
	if err := c.doJSON(ctx, http.MethodGet, "/components/"+url.PathEscape(id), nil, &out); err != nil {
		return hosts.Component{}, hostRouteError(err)
	}
	return out, nil
}
