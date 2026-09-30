package webui

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// Console writes (M9) are made by the serving process, where the approval
// broker is: the console sends each one there, as it does a resource verb.
// Each has the confirm step's two routes beside it (P3-3b): …/plan, the
// write's plan and hash, and …/confirm, the write confirmed against that
// plan. A write retried after an out-of-band approval names the approval.

// consoleConfirm is what a console write's body carries beside the write:
// the approval it runs under, and on a confirm route the plan confirmed.
type consoleConfirm struct {
	ApprovalID        string `json:"approval_id"`
	ConfirmedPlanHash string `json:"confirmed_plan_hash"`
}

// consoleRoute splits a trailing /plan or /confirm off path.
func consoleRoute(path string) (rest, route string) {
	for _, suffix := range []string{"plan", "confirm"} {
		if trimmed, ok := strings.CutSuffix(path, "/"+suffix); ok {
			return trimmed, suffix
		}
	}
	return path, ""
}

// consoleWrite sends req to the serving process on route and answers
// everything but a write that ran: a plan, a refusal (an approval to meet
// among them, with its channel) and a failure. It returns the result of a
// write that ran, or false when it has answered.
func (s *Server) consoleWrite(w http.ResponseWriter, r *http.Request, route string, confirm consoleConfirm, req cerbapi.ConsoleWriteRequest) (*cerbapi.ConsoleWriteResult, bool) {
	if route == "plan" {
		p, err := s.client.PlanConsoleWrite(r.Context(), req)
		if err != nil {
			writeConsoleWriteError(w, err)
			return nil, false
		}
		writePlan(w, p)
		return nil, false
	}
	opts := []cerbapi.MutationOption{cerbapi.WithApprovalID(confirm.ApprovalID)}
	if route == "confirm" {
		if confirm.ConfirmedPlanHash == "" {
			writeError(w, http.StatusBadRequest, errConfirmWithoutHash)
			return nil, false
		}
		opts = append(opts, cerbapi.WithConfirmedPlanHash(confirm.ConfirmedPlanHash))
	}
	result, err := s.client.ConsoleWrite(r.Context(), req, opts...)
	if err != nil {
		writeConsoleWriteError(w, err)
		return nil, false
	}
	if result == nil {
		writeError(w, http.StatusBadGateway, "the serving Cerberus returned no result; restart the daemon on this build")
		return nil, false
	}
	return result, true
}

// writeConsoleWriteError answers a console write that did not run. Over the
// socket the daemon's status travels with the error; an in-process client's
// errors are mapped here the same way: not found 404, a refused input 400.
func writeConsoleWriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cerbapi.ErrConsoleWriteNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.As(err, new(cerbapi.ConsoleWriteInputError)):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeClientError(w, err)
	}
}
