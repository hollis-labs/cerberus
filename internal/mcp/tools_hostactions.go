package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// The Outpost host actions: main's tools_host_actions.go over POST
// /api/hosts/{name}/actions/{verb}[/plan]. They answer with the route's result as is (the outpost
// result, or the plan an approval binds to), as main's did.

func init() {
	for _, verb := range []string{"units", "logs", "start", "stop", "restart", "plan"} {
		handlers["cerberus_host_"+verb] = hostActionTool(verb)
	}
}

func hostActionTool(verb string) handler {
	return func(ctx context.Context, b Backend, args map[string]any) (any, error) {
		operation, path := verb, ""
		if verb == "plan" {
			operation = str(args, "verb")
			path = "/plan"
		}
		req := map[string]any{"verb": operation, "unit": str(args, "unit")}
		if n, ok := args["lines"].(float64); ok {
			req["lines"] = int(n)
		}
		body := map[string]any{"request": req, "acknowledged": boolArg(args, "acknowledged")}
		if a := str(args, "approval_id"); a != "" {
			body["approval_id"] = a
		}
		var out json.RawMessage
		if err := b.Do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(str(args, "name"))+"/actions/"+url.PathEscape(operation)+path, body, &out); err != nil {
			return nil, err
		}
		var v any
		if err := json.Unmarshal(out, &v); err != nil {
			return nil, err
		}
		return v, nil
	}
}
