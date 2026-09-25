package cerbapi

import (
	"errors"
	"fmt"
	"net/http"
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
var externalConnectorHTTPStatus = map[ExternalConnectorErrorCode]int{
	ExternalConnectorInvalidArgs:        http.StatusBadRequest,
	ExternalConnectorAckRequired:        http.StatusConflict,
	ExternalConnectorPreviewUnsupported: http.StatusUnprocessableEntity,
	ExternalConnectorUnsupported:        http.StatusNotFound,
	ExternalConnectorUnavailable:        http.StatusServiceUnavailable,
	ExternalConnectorCredentialMissing:  http.StatusServiceUnavailable,
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
}

func connectorErrorWireFor(err error) connectorErrorWire {
	var connErr *ExternalConnectorError
	if !errors.As(err, &connErr) {
		return connectorErrorWire{}
	}
	wire := connectorErrorWire{Code: connErr.Code, Connector: connErr.Connector, Operation: connErr.Operation}
	if connErr.Err != nil {
		wire.Detail = connErr.Err.Error()
	}
	return wire
}

// daemonError rebuilds the error the daemon reported. A coded refusal comes
// back as an *ExternalConnectorError, so errors.As works on the client side
// of the socket exactly as it does in-process.
func daemonError(msg string, wire connectorErrorWire) error {
	if wire.Code == "" {
		return fmt.Errorf("daemon: %s", msg)
	}
	connErr := &ExternalConnectorError{Code: wire.Code, Connector: wire.Connector, Operation: wire.Operation}
	if wire.Detail != "" {
		connErr.Err = errors.New(wire.Detail)
	}
	return fmt.Errorf("daemon: %w", connErr)
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
