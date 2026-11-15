package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hollis-labs/cerberus/internal/api"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
)

// cerberus_resource_ensure_fresh is main's cerbapi.EnsureFresh composed over /api, as main's
// client composed it over the socket: read the status, then run the recommended deploy, apply or
// sync (each through L7 and the gate), or nothing.

func init() { handlers["cerberus_resource_ensure_fresh"] = ensureFresh }

// ensureFreshResult is main's cerbapi.EnsureFreshResult.
type ensureFreshResult struct {
	ServiceID string        `json:"service_id"`
	Result    *api.OpResult `json:"result,omitempty"`
	Action    string        `json:"action"`
	Reason    string        `json:"reason,omitempty"`
	Message   string        `json:"message,omitempty"`
	Success   bool          `json:"success"`
}

func ensureFresh(ctx context.Context, b Backend, args map[string]any) (any, error) {
	id := str(args, "resource_id")
	if id == "" {
		return required("resource_id")
	}
	run := func(verb, reason string) (*ensureFreshResult, error) {
		body := map[string]any{"acknowledged": boolArg(args, "acknowledged")}
		if a := str(args, "approval_id"); a != "" {
			body["approval_id"] = a
		}
		var op api.OpResult
		if err := b.Do(ctx, http.MethodPost, "/resources/"+url.PathEscape(id)+"/"+verb, body, &op); err != nil {
			return nil, err
		}
		r := &ensureFreshResult{ServiceID: id, Action: verb, Reason: reason, Result: &op, Success: op.Success, Message: op.Message}
		if !op.Success && op.Error != "" {
			r.Message = op.Error
		}
		return r, nil
	}
	var res *ensureFreshResult
	var err error
	if force, _ := args["force"].(bool); force {
		res, err = run("deploy", "forced rebuild")
	} else {
		var st api.ResourceRuntimeStatus
		if err = b.Do(ctx, http.MethodGet, "/resources/"+url.PathEscape(id)+"/status", nil, &st); err != nil {
			return nil, err
		}
		switch st.RecommendedAction {
		case "deploy", "apply", "sync":
			res, err = run(st.RecommendedAction, st.RecommendedReason)
		case "":
			res = &ensureFreshResult{ServiceID: id, Action: "noop", Success: true,
				Message: "no built-binary drift detected; source freshness is not checked; use deploy or ensure-fresh --force after source changes"}
		case "inspect":
			res = &ensureFreshResult{ServiceID: id, Action: "noop", Reason: st.RecommendedReason,
				Message: localconn.RecommendedNextStep("inspect", st.RecommendedReason)}
		default:
			res = &ensureFreshResult{ServiceID: id, Action: "noop", Reason: st.RecommendedReason,
				Message: fmt.Sprintf("unsupported recommended action %q; inspect the resource before changing it", st.RecommendedAction)}
		}
	}
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, toolFailure{message: res.Message, content: res}
	}
	return indented(res)
}
