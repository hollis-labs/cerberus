package cerbapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/hollis-labs/cerberus/pkg/outpost"
)

// Action planning has a distinct route, so an older daemon cannot ignore a
// restrictive body flag and run the operation instead.
func hostActionHTTP(w http.ResponseWriter, r *http.Request, client HostClient, parts []string) bool {
	if len(parts) < 3 || parts[1] != "actions" {
		return false
	}
	if (len(parts) != 3 && len(parts) != 4) || (len(parts) == 4 && parts[3] != "plan") || r.Method != "POST" {
		writeJSONError(w, http.StatusNotFound, "no such host action route")
		return true
	}
	c, ok := client.(HostActionClient)
	if !ok {
		writeServiceError(w, http.StatusNotImplemented, errHostsUnavailable)
		return true
	}
	var args HostActionArgs
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&args); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid host action body")
		return true
	}
	args.Request.Verb = parts[2]
	if len(parts) == 4 {
		plan, err := c.PlanHostAction(r.Context(), unescape(parts[0]), args)
		if err != nil {
			writeServiceError(w, hostStatus(err), err)
		} else {
			writeJSON(w, http.StatusOK, plan)
		}
	} else {
		result, err := c.ExecuteHostAction(r.Context(), unescape(parts[0]), args)
		if err != nil {
			writeServiceError(w, hostStatus(err), err)
		} else {
			writeJSON(w, http.StatusOK, result)
		}
	}
	return true
}

func (c *SocketClient) PlanHostAction(ctx context.Context, name string, args HostActionArgs) (*ConnectorPlan, error) {
	var out ConnectorPlan
	path := "/hosts/" + url.PathEscape(name) + "/actions/" + url.PathEscape(args.Request.Verb) + "/plan"
	if err := c.doJSON(ctx, http.MethodPost, path, args, &out); err != nil {
		return nil, hostRouteError(err)
	}
	return &out, nil
}
func (c *SocketClient) ExecuteHostAction(ctx context.Context, name string, args HostActionArgs) (outpost.Result, error) {
	var out outpost.Result
	path := "/hosts/" + url.PathEscape(name) + "/actions/" + url.PathEscape(args.Request.Verb)
	if err := c.doJSON(ctx, http.MethodPost, path, args, &out); err != nil {
		return out, hostRouteError(err)
	}
	return out, nil
}
