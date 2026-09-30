package webui

import (
	"context"
	"errors"
	"net/http"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/infra"
)

// errConsoleNotFound is a console write whose subject does not exist.
var errConsoleNotFound = errors.New("not found")

// badRequest is a console write refused for its input: 400, as before the
// write was gated.
type badRequest struct{ err error }

func (e badRequest) Error() string { return e.err.Error() }
func (e badRequest) Unwrap() error { return e.err }

// consoleWrite runs do as a gated, recorded console write (M9) and answers
// its failure: the gate's refusal as the daemon's would be, not found as
// 404, anything else as 500. It reports whether the write happened.
func (s *Server) consoleWrite(w http.ResponseWriter, r *http.Request, cw cerbapi.ConsoleWrite, do func(context.Context) error) bool {
	err := cerbapi.RunConsoleWrite(r.Context(), s.audit, cw, do)
	var coded *cerbapi.ExternalConnectorError
	switch {
	case err == nil:
		return true
	case errors.As(err, &coded):
		writeClientError(w, err)
	case errors.Is(err, errConsoleNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.As(err, new(badRequest)):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
	return false
}

// profileTarget names a deploy profile and its labels for the record, in
// the clear: relabelling a target is what changes its approval channel.
func profileTarget(p infra.DeploymentProfile) map[string]any {
	labels := p.ResourceDef().TargetLabels().Labels
	return map[string]any{"id": p.ID, "env": string(labels.Env), "owner": labels.Owner, "admin": labels.Admin.String()}
}
