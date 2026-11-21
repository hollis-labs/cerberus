package cerbapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// FleetSnapshotClient is separate so existing HostClient implementations keep
// their monitoring capabilities when this read is unavailable.
type FleetSnapshotClient interface {
	FleetSnapshot(context.Context, FleetSnapshotQuery) (FleetSnapshot, error)
}

func FleetSnapshotClientOf(client any) FleetSnapshotClient {
	if c, ok := client.(FleetSnapshotClient); ok {
		return c
	}
	return unavailableHostClient{}
}
func (unavailableHostClient) FleetSnapshot(context.Context, FleetSnapshotQuery) (FleetSnapshot, error) {
	return FleetSnapshot{}, errMonitoringUnavailable
}
func (c *InProcessClient) FleetSnapshot(ctx context.Context, q FleetSnapshotQuery) (FleetSnapshot, error) {
	s, err := c.hostService()
	if err != nil {
		return FleetSnapshot{}, err
	}
	return s.FleetSnapshot(ctx, q)
}
func (c *SocketClient) FleetSnapshot(ctx context.Context, q FleetSnapshotQuery) (FleetSnapshot, error) {
	var out FleetSnapshot
	v := url.Values{"offset": {strconv.Itoa(q.Offset)}, "limit": {strconv.Itoa(q.Limit)}}
	if err := c.doJSON(ctx, http.MethodGet, "/hosts/_fleet_snapshot?"+v.Encode(), nil, &out); err != nil {
		return FleetSnapshot{}, hostRouteError(err)
	}
	return out, nil
}

func fleetSnapshotHTTP(w http.ResponseWriter, r *http.Request, client HostClient, parts []string) bool {
	if r.Method != http.MethodGet || len(parts) != 1 || parts[0] != "_fleet_snapshot" {
		return false
	}
	q := FleetSnapshotQuery{}
	var err error
	for key, dst := range map[string]*int{"offset": &q.Offset, "limit": &q.Limit} {
		if raw := r.URL.Query().Get(key); raw != "" {
			*dst, err = strconv.Atoi(raw)
			if err != nil {
				writeServiceError(w, http.StatusBadRequest, &HostInvalidError{Err: redact.Guidance("%s must be an integer", key)})
				return true
			}
		}
	}
	out, err := FleetSnapshotClientOf(client).FleetSnapshot(r.Context(), q)
	if err != nil {
		err = scopeError(redact.ScopeFrom(r.Context()), err)
		writeServiceError(w, hostStatus(err), err)
		return true
	}
	writeJSON(w, http.StatusOK, out)
	return true
}
