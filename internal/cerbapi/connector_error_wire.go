package cerbapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// externalConnectorHTTPStatus is the one table from a connector error code to
// the HTTP status a surface answers with. The socket server and the web
// console both read it, and a test holds it to externalConnectorErrorCodes,
// so a new code cannot fall through to 500.
//
//   - invalid_args: the caller sent something wrong; fix the request.
//   - acknowledgment_required: the request is well formed but conflicts with
//     the gate; resend it acknowledged.
//   - preview_unsupported: well formed, but a dry run of it cannot be
//     produced. Nothing was executed.
//   - operation_unsupported: no such operation here.
//   - connector_unavailable, credential_missing: the connector cannot serve
//     right now; the request may succeed once the operator fixes that.
//   - plugin_changed: the plugin is not the bundle the operator reviewed;
//     it conflicts with the accepted review until re-accepted on a TTY.
//   - principal_refused: the socket caller is not the daemon's own user.
//   - policy_denied, approval_required, approval_pending, approval_expired,
//     plan_stale: reserved for P3 policy enforcement, returned by nothing
//     yet.
//   - operation_failed: the request passed every gate and the provider, the
//     plugin or the tool behind the connector failed it. 502, so a 500 still
//     means a fault in Cerberus itself.
var externalConnectorHTTPStatus = map[ExternalConnectorErrorCode]int{
	ExternalConnectorInvalidArgs:        http.StatusBadRequest,
	ExternalConnectorAckRequired:        http.StatusConflict,
	ExternalConnectorPreviewUnsupported: http.StatusUnprocessableEntity,
	ExternalConnectorUnsupported:        http.StatusNotFound,
	ExternalConnectorUnavailable:        http.StatusServiceUnavailable,
	ExternalConnectorCredentialMissing:  http.StatusServiceUnavailable,
	ExternalConnectorOperationFailed:    http.StatusBadGateway,
	ExternalConnectorAuditUnavailable:   http.StatusServiceUnavailable,
	ExternalConnectorPluginChanged:      http.StatusConflict,
	ExternalConnectorPrincipalRefused:   http.StatusForbidden,
	// Reserved for P3 enforcement; nothing returns these yet.
	ExternalConnectorPolicyDenied:     http.StatusForbidden,
	ExternalConnectorApprovalRequired: http.StatusPreconditionRequired,
	ExternalConnectorApprovalPending:  http.StatusConflict,
	ExternalConnectorApprovalExpired:  http.StatusConflict,
	ExternalConnectorPlanStale:        http.StatusConflict,
}

// ExternalConnectorHTTPStatus returns the status for err when it carries a
// connector error code, and fallback otherwise.
func ExternalConnectorHTTPStatus(err error, fallback int) int {
	var connErr *ExternalConnectorError
	if errors.As(err, &connErr) {
		if status, ok := externalConnectorHTTPStatus[connErr.Code]; ok {
			return status
		}
	}
	return fallback
}

// connectorErrorWire is the machine-readable half of a connector refusal on
// the socket. Without it the client can only rebuild a string, and every
// caller downstream — the web console's status mapping, the CLI's recovery
// hints — loses the code the daemon had in hand.
type connectorErrorWire struct {
	Code      ExternalConnectorErrorCode `json:"code,omitempty"`
	Connector string                     `json:"connector,omitempty"`
	Operation string                     `json:"operation,omitempty"`
	Detail    string                     `json:"detail,omitempty"`
	// Approval is approval_pending's {id, expires_at, approve_with}.
	Approval *ApprovalRef `json:"approval,omitempty"`

	// Rendered says the daemon rendered this error's text once, where it
	// was made: Cerberus's prose kept, the detail redacted. A client trusts
	// the text as final only when it is set, so a client talking to a
	// daemon that predates it, or the reverse, falls back to running the
	// rules — never to skipping them on text nobody rendered.
	Rendered bool `json:"rendered,omitempty"`
}

func connectorErrorWireFor(err error) connectorErrorWire {
	var connErr *ExternalConnectorError
	if !errors.As(err, &connErr) {
		return connectorErrorWire{}
	}
	wire := connectorErrorWire{Code: connErr.Code, Connector: connErr.Connector, Operation: connErr.Operation, Approval: connErr.Approval}
	if connErr.Err != nil {
		wire.Detail = connErr.Err.Error()
	}
	return wire
}

// daemonError rebuilds the error the daemon reported. A coded refusal comes
// back as an *ExternalConnectorError, so errors.As works on the client side
// of the socket exactly as it does in-process.
//
// When the daemon marked the text rendered, the error is PreRendered: an edge
// on this side shows it with the scope's values removed and no rules. Without
// the marker it is ordinary text, and the rules run over it as before.
func daemonError(msg string, wire connectorErrorWire) error {
	if wire.Code == "" {
		if wire.Rendered {
			return renderedDaemonError{text: "daemon: " + msg}
		}
		return fmt.Errorf("daemon: %s", msg)
	}
	connErr := &ExternalConnectorError{Code: wire.Code, Connector: wire.Connector, Operation: wire.Operation, Approval: wire.Approval, rendered: wire.Rendered}
	if wire.Detail != "" {
		if wire.Rendered {
			connErr.Err = renderedDaemonError{text: wire.Detail}
		} else {
			connErr.Err = errors.New(wire.Detail)
		}
	}
	if wire.Rendered {
		return renderedDaemonError{text: "daemon: " + connErr.Error(), cause: connErr}
	}
	return fmt.Errorf("daemon: %w", connErr)
}

// renderedDaemonError is text the daemon rendered and said so. It is its own
// Renderer, so nothing on this side runs the rules over it again.
type renderedDaemonError struct {
	text  string
	cause error
}

func (e renderedDaemonError) Error() string                         { return e.text }
func (e renderedDaemonError) Unwrap() error                         { return e.cause }
func (e renderedDaemonError) PreRendered() bool                     { return true }
func (e renderedDaemonError) RenderRedacted(s *redact.Scope) string { return s.ReplaceValues(e.text) }

// markRendered sets the wire's rendered flag when the text going out is text
// this request rendered (redact.Scope.IsRendered). Called by the socket's
// writers, so no call site has to know.
func (w *connectorErrorWire) markRendered(scope *redact.Scope, text string) {
	w.Rendered = scope.IsRendered(text)
	if w.Detail != "" && !scope.IsRendered(w.Detail) {
		w.Rendered = false
	}
}

// errorResponseFor is the socket's error body for err.
func errorResponseFor(err error) ErrorResponse {
	return ErrorResponse{Success: false, Error: err.Error(), connectorErrorWire: connectorErrorWireFor(err)}
}

// writeServiceError answers with err's connector status, or fallback when err
// carries no code, and keeps the code on the wire.
func writeServiceError(w http.ResponseWriter, fallback int, err error) {
	writeJSON(w, ExternalConnectorHTTPStatus(err, fallback), errorResponseFor(err))
}
