package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/gate"
)

// The brake routes are the old socket's /brakes routes (internal/cerbapi brakes.go handleBrakes
// on main) under /api, with the old bodies: the state as gate.BrakesView, engaging with
// gate.BrakeEngageArgs, lifting with gate.BrakeLiftArgs, a reset with gate.BrakeResetArgs.
// Errors use the flat body: a gate refusal with the old status for its code and the approval it
// names under details (approval_pending on a lift), a brake that is not engaged the old 409.
//
// Who may do what is the gate's, as it was the old daemon's: anyone holding cerberus.admin
// engages (an agent may stop itself); a lift is a person's, with the owner's out-of-band
// approval decided on the console; a suspension is reset by a person typing "reset <id>". The
// only person on this API is the owner's console session (GatePrincipal), so an API key is
// refused a lift or a reset with 428 approval_required, as the old daemon refused an agent.

// brakes is the gate's brakes, or false after answering that there are none: observe mode,
// where the old Cerberus holds them (the approvals routes' 503).
func (a *API) brakes(w http.ResponseWriter) (*gate.Gate, *gate.Brakes, bool) {
	if a.o.Gate == nil || a.o.Gate.Brakes() == nil {
		apierr.Write(w, http.StatusServiceUnavailable, "brakes_unavailable",
			"this Cerberus holds no brakes: in observe mode the old Cerberus holds them until the handover", nil)
		return nil, nil, false
	}
	return a.o.Gate, a.o.Gate.Brakes(), true
}

// brakesGet is GET /api/brakes: the effective state and what reading it found wrong.
func (a *API) brakesGet(w http.ResponseWriter, _ *http.Request) {
	_, b, ok := a.brakes(w)
	if !ok {
		return
	}
	writeJSON(w, gate.BrakesView{State: b.Current(), Problems: b.Problems()})
}

// brakeAction decodes the body into args as the old socket did (empty is the zero value, at
// most 1 MiB), runs do, and answers with the state after or the old refusal.
func (a *API) brakeAction(w http.ResponseWriter, r *http.Request, args any, do func(*gate.Gate) (brake.State, error)) {
	g, _, ok := a.brakes(w)
	if !ok {
		return
	}
	if err := decodeBody(r.Body, args); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
		return
	}
	st, err := do(g)
	switch {
	case errors.Is(err, brake.ErrNotEngaged):
		apierr.Write(w, http.StatusConflict, "not_engaged", "that brake is not engaged; `cerberus status` shows what is", nil)
	case err != nil:
		writeGateErr(w, err)
	default:
		writeJSON(w, gate.BrakesView{State: st})
	}
}

// brakeLockdown engages the lockdown. A match in the body is ignored, as the old route did.
func (a *API) brakeLockdown(w http.ResponseWriter, r *http.Request) {
	var args gate.BrakeEngageArgs
	a.brakeAction(w, r, &args, func(g *gate.Gate) (brake.State, error) {
		st, _, err := g.EngageLockdown(GateContext(r.Context()), args.Reason)
		return st, err
	})
}

// brakeFreeze engages a freeze on the body's match, which it needs.
func (a *API) brakeFreeze(w http.ResponseWriter, r *http.Request) {
	var args gate.BrakeEngageArgs
	a.brakeAction(w, r, &args, func(g *gate.Gate) (brake.State, error) {
		if args.Match == nil {
			return brake.State{}, fmt.Errorf("%w: a freeze needs a match", errBadArgs)
		}
		st, _, err := g.EngageFreeze(GateContext(r.Context()), *args.Match, args.Reason)
		return st, err
	})
}

// brakeLiftLockdown lifts the lockdown: the first call asks for the owner's approval
// (409 approval_pending naming it), the call naming it with approval_id lifts.
func (a *API) brakeLiftLockdown(w http.ResponseWriter, r *http.Request) {
	var args gate.BrakeLiftArgs
	a.brakeAction(w, r, &args, func(g *gate.Gate) (brake.State, error) {
		return g.LiftLockdown(GateContext(r.Context()), args.ApprovalID)
	})
}

// brakeLiftFreeze lifts the freeze {id}, as brakeLiftLockdown.
func (a *API) brakeLiftFreeze(w http.ResponseWriter, r *http.Request) {
	var args gate.BrakeLiftArgs
	a.brakeAction(w, r, &args, func(g *gate.Gate) (brake.State, error) {
		return g.LiftFreeze(GateContext(r.Context()), r.PathValue("id"), args.ApprovalID)
	})
}

// brakeResetSuspension resets the circuit breaker's suspension {id}. The typed phrase travels
// in the body and the gate checks it, so no surface can skip it.
func (a *API) brakeResetSuspension(w http.ResponseWriter, r *http.Request) {
	var args gate.BrakeResetArgs
	a.brakeAction(w, r, &args, func(g *gate.Gate) (brake.State, error) {
		return g.ResetSuspension(GateContext(r.Context()), r.PathValue("id"), args.Typed, args.ApprovalID)
	})
}
