package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// The emergency brake on the console (§12): engaging is one click and no
// typed phrase; lifting goes through the daemon, with a passkey where one
// is enrolled (the approvals page approves it, then this lifts it).

type brakesClient interface {
	Brakes(ctx context.Context) (cerbapi.BrakesView, error)
	EngageBrake(ctx context.Context, args cerbapi.BrakeEngageArgs) (cerbapi.BrakesView, error)
	LiftBrake(ctx context.Context, freezeID string, args cerbapi.BrakeLiftArgs) (cerbapi.BrakesView, error)
	ResetSuspension(ctx context.Context, id string, args cerbapi.BrakeResetArgs) (cerbapi.BrakesView, error)
}

// brakesState is the header's brake banner: nil when nothing is braked or
// the daemon cannot say.
func (s *Server) brakesState(ctx context.Context) *brake.State {
	c, ok := s.client.(brakesClient)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	view, err := c.Brakes(ctx)
	if err != nil || (!view.State.Engaged() && len(view.State.Suspensions) == 0) {
		return nil
	}
	return &view.State
}

// handleBrakes is GET /api/brakes, POST /api/brakes/lockdown (engage),
// /api/brakes/lockdown/lift and /api/brakes/freeze/{id}/lift.
func (s *Server) handleBrakes(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/brakes"), "/")
	switch {
	case r.Method == http.MethodGet && rest == "":
	case r.Method != http.MethodPost || rest == "":
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	case !s.allowStateChangingRequest(r):
		writeError(w, http.StatusForbidden, "state-changing request rejected")
		return
	}
	c, ok := s.client.(brakesClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "this console is not connected to a daemon that holds the brakes")
		return
	}
	if r.Method == http.MethodGet {
		view, err := c.Brakes(r.Context())
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	var (
		view cerbapi.BrakesView
		err  error
	)
	switch {
	case rest == "lockdown":
		var args cerbapi.BrakeEngageArgs
		if err = decodeJSONBody(r, &args); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		args.Match = nil
		view, err = c.EngageBrake(r.Context(), args)
	case rest == "lockdown/lift":
		var args cerbapi.BrakeLiftArgs
		if err = decodeJSONBody(r, &args); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		view, err = c.LiftBrake(r.Context(), "", args)
	case strings.HasPrefix(rest, "freeze/") && strings.HasSuffix(rest, "/lift"):
		var args cerbapi.BrakeLiftArgs
		if err = decodeJSONBody(r, &args); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		view, err = c.LiftBrake(r.Context(), strings.TrimSuffix(strings.TrimPrefix(rest, "freeze/"), "/lift"), args)
	case strings.HasPrefix(rest, "suspensions/") && strings.HasSuffix(rest, "/reset"):
		// The typed phrase travels to the daemon, which checks it.
		var args cerbapi.BrakeResetArgs
		if err = decodeJSONBody(r, &args); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		view, err = c.ResetSuspension(r.Context(), strings.TrimSuffix(strings.TrimPrefix(rest, "suspensions/"), "/reset"), args)
	default:
		writeError(w, http.StatusNotFound, "expected /api/brakes, /api/brakes/lockdown[/lift], /api/brakes/freeze/{id}/lift or /api/brakes/suspensions/{id}/reset")
		return
	}
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
