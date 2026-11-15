package api

import (
	"encoding/json"
	"net/http"

	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/hostmon"
)

// The Outpost host actions (main's cerbapi host_actions_http.go): POST
// /api/hosts/{name}/actions/{verb} runs a unit operation through the gate, and its /plan form
// answers the plan an approval binds to. The verb comes from the path; the body is main's
// {request, acknowledged, approval_id}, at most 64 KiB, unknown fields refused. Scopes follow the
// effect: units and logs (read-sensitive) and every plan need cerberus.read, start, stop and
// restart (lifecycle) cerberus.operate. Observe mode runs no action.

func (a *API) hostAction(verb string, planOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.o.Gate == nil || a.o.HostActions == nil {
			apierr.Write(w, http.StatusConflict, "observe_mode", "this Cerberus is in observe mode, so it runs no host action: the old Cerberus still does", nil)
			return
		}
		var args hostmon.ActionArgs
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		d.DisallowUnknownFields()
		if err := d.Decode(&args); err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", "invalid host action body", nil)
			return
		}
		args.Request.Verb = verb
		ctx := GateContext(r.Context())
		name := r.PathValue("name")
		if planOnly {
			p, err := a.o.HostActions.Plan(ctx, name, args)
			if err != nil {
				hostErr(w, err)
				return
			}
			writeScoped(ctx, w, p)
			return
		}
		res, err := a.o.HostActions.Execute(ctx, name, args)
		if err != nil {
			hostErr(w, err)
			return
		}
		writeScoped(ctx, w, res)
	}
}

func (a *API) hostActionRoutes() []Route {
	var rs []Route
	for verb, scope := range map[string]string{"units": ScopeRead, "logs": ScopeRead, "start": ScopeOperate, "stop": ScopeOperate, "restart": ScopeOperate} {
		rs = append(rs,
			Route{http.MethodPost, "/api/hosts/{name}/actions/" + verb, scope, a.hostAction(verb, false)},
			Route{http.MethodPost, "/api/hosts/{name}/actions/" + verb + "/plan", ScopeRead, a.hostAction(verb, true)},
		)
	}
	return rs
}
