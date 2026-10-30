package cerbapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// HostMonitorClient is the monitoring reads (Hosts E): status, samples and the
// monitor's schedule. Separate from HostClient so older fakes keep compiling.
type HostMonitorClient interface {
	HostStatus(ctx context.Context, name string) (HostStatusView, error)
	AllHostStatus(ctx context.Context) ([]HostStatusView, error)
	HostSamples(ctx context.Context, name string, q SamplesQuery) ([]hosts.Sample, error)
	HostRollups(ctx context.Context, name, grain string, q SamplesQuery) ([]hosts.Rollup, error)
	MonitorState(ctx context.Context) (MonitorSnapshot, error)
}

// HostMonitorClientOf returns client as a HostMonitorClient, or one that
// refuses every call with the reason.
func HostMonitorClientOf(client any) HostMonitorClient {
	if m, ok := client.(HostMonitorClient); ok {
		return m
	}
	return unavailableHostClient{}
}

func (unavailableHostClient) HostStatus(context.Context, string) (HostStatusView, error) {
	return HostStatusView{}, errHostsUnavailable
}
func (unavailableHostClient) AllHostStatus(context.Context) ([]HostStatusView, error) {
	return nil, errHostsUnavailable
}
func (unavailableHostClient) HostSamples(context.Context, string, SamplesQuery) ([]hosts.Sample, error) {
	return nil, errHostsUnavailable
}
func (unavailableHostClient) HostRollups(context.Context, string, string, SamplesQuery) ([]hosts.Rollup, error) {
	return nil, errHostsUnavailable
}
func (unavailableHostClient) MonitorState(context.Context) (MonitorSnapshot, error) {
	return MonitorSnapshot{}, errHostsUnavailable
}

// ParseSamplesTime reads a time bound: an RFC 3339 time, or a duration such as
// 6h meaning that long ago.
func ParseSamplesTime(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, &HostInvalidError{Err: redact.Guidance("%q is not a time: use RFC 3339 or a duration such as 6h", s)}
}

// hostMonitorHTTP serves the monitoring reads. It reports whether it handled
// the request.
func hostMonitorHTTP(w http.ResponseWriter, r *http.Request, client HostClient, parts []string) bool {
	if r.Method != http.MethodGet {
		return false
	}
	mc := HostMonitorClientOf(client)
	fail := func(err error) {
		err = scopeError(redact.ScopeFrom(r.Context()), err)
		writeServiceError(w, hostStatus(err), err)
	}
	switch {
	case len(parts) == 1 && parts[0] == "_monitor":
		v, err := mc.MonitorState(r.Context())
		if err != nil {
			fail(err)
			return true
		}
		writeJSON(w, http.StatusOK, v)
	case len(parts) == 1 && parts[0] == "_status":
		v, err := mc.AllHostStatus(r.Context())
		if err != nil {
			fail(err)
			return true
		}
		if v == nil {
			v = []HostStatusView{}
		}
		writeJSON(w, http.StatusOK, v)
	case len(parts) == 2 && parts[1] == "status":
		v, err := mc.HostStatus(r.Context(), unescape(parts[0]))
		if err != nil {
			fail(err)
			return true
		}
		writeJSON(w, http.StatusOK, v)
	case len(parts) == 2 && parts[1] == "samples":
		now := time.Now()
		q := SamplesQuery{Grain: r.URL.Query().Get("grain")}
		var err error
		if q.Since, err = ParseSamplesTime(r.URL.Query().Get("since"), now); err != nil {
			fail(err)
			return true
		}
		if q.Until, err = ParseSamplesTime(r.URL.Query().Get("until"), now); err != nil {
			fail(err)
			return true
		}
		q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
		if q.Grain != "" && q.Grain != "raw" {
			rs, err := mc.HostRollups(r.Context(), unescape(parts[0]), q.Grain, q)
			if err != nil {
				fail(err)
				return true
			}
			if rs == nil {
				rs = []hosts.Rollup{}
			}
			writeJSON(w, http.StatusOK, rs)
			return true
		}
		v, err := mc.HostSamples(r.Context(), unescape(parts[0]), q)
		if err != nil {
			fail(err)
			return true
		}
		if v == nil {
			v = []hosts.Sample{}
		}
		writeJSON(w, http.StatusOK, v)
	default:
		return false
	}
	return true
}

// ---- in-process client ----

func (c *InProcessClient) HostStatus(ctx context.Context, name string) (HostStatusView, error) {
	s, err := c.hostService()
	if err != nil {
		return HostStatusView{}, err
	}
	return s.HostStatus(ctx, name)
}
func (c *InProcessClient) AllHostStatus(ctx context.Context) ([]HostStatusView, error) {
	s, err := c.hostService()
	if err != nil {
		return nil, err
	}
	return s.AllHostStatus(ctx)
}
func (c *InProcessClient) HostSamples(ctx context.Context, name string, q SamplesQuery) ([]hosts.Sample, error) {
	s, err := c.hostService()
	if err != nil {
		return nil, err
	}
	return s.HostSamples(ctx, name, q)
}
func (c *InProcessClient) HostRollups(ctx context.Context, name, grain string, q SamplesQuery) ([]hosts.Rollup, error) {
	s, err := c.hostService()
	if err != nil {
		return nil, err
	}
	return s.HostRollups(ctx, name, grain, q)
}
func (c *InProcessClient) MonitorState(ctx context.Context) (MonitorSnapshot, error) {
	s, err := c.hostService()
	if err != nil {
		return MonitorSnapshot{}, err
	}
	return s.MonitorState(ctx)
}

// ---- socket client ----

func (c *SocketClient) HostStatus(ctx context.Context, name string) (HostStatusView, error) {
	var out HostStatusView
	if err := c.doJSON(ctx, http.MethodGet, "/hosts/"+url.PathEscape(name)+"/status", nil, &out); err != nil {
		return HostStatusView{}, hostRouteError(err)
	}
	return out, nil
}
func (c *SocketClient) AllHostStatus(ctx context.Context) ([]HostStatusView, error) {
	var out []HostStatusView
	if err := c.doJSON(ctx, http.MethodGet, "/hosts/_status", nil, &out); err != nil {
		return nil, hostRouteError(err)
	}
	return out, nil
}
func (c *SocketClient) HostSamples(ctx context.Context, name string, q SamplesQuery) ([]hosts.Sample, error) {
	v := url.Values{}
	if !q.Since.IsZero() {
		v.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	if !q.Until.IsZero() {
		v.Set("until", q.Until.UTC().Format(time.RFC3339))
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Grain != "" {
		v.Set("grain", q.Grain)
	}
	path := "/hosts/" + url.PathEscape(name) + "/samples"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var out []hosts.Sample
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, hostRouteError(err)
	}
	return out, nil
}
func (c *SocketClient) HostRollups(ctx context.Context, name, grain string, q SamplesQuery) ([]hosts.Rollup, error) {
	v := url.Values{"grain": {grain}}
	if !q.Since.IsZero() {
		v.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	if !q.Until.IsZero() {
		v.Set("until", q.Until.UTC().Format(time.RFC3339))
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	var out []hosts.Rollup
	if err := c.doJSON(ctx, http.MethodGet, "/hosts/"+url.PathEscape(name)+"/samples?"+v.Encode(), nil, &out); err != nil {
		return nil, hostRouteError(err)
	}
	return out, nil
}
func (c *SocketClient) MonitorState(ctx context.Context) (MonitorSnapshot, error) {
	var out MonitorSnapshot
	if err := c.doJSON(ctx, http.MethodGet, "/hosts/_monitor", nil, &out); err != nil {
		return MonitorSnapshot{}, hostRouteError(err)
	}
	return out, nil
}
