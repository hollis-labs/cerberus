package gate

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// ErrorCode is the machine-readable half of every refusal the gate returns
// (the old build's ExternalConnectorErrorCode, verbatim values).
type ErrorCode string

const (
	CodeUnavailable       ErrorCode = "connector_unavailable"
	CodeCredentialMissing ErrorCode = "credential_missing" //nolint:gosec // an error code, not a credential
	CodeUnsupported       ErrorCode = "operation_unsupported"
	CodeInvalidArgs       ErrorCode = "invalid_args"
	CodeAckRequired       ErrorCode = "acknowledgment_required"
	// CodePreviewUnsupported is a dry run of an operation that has no
	// preview. The operation is refused, never executed.
	CodePreviewUnsupported ErrorCode = "preview_unsupported"
	// CodeOperationFailed is a connector operation — built-in or plugin —
	// that passed every gate and then failed in the provider, the plugin or
	// the tool it drives, for a reason the host did not classify. It is not
	// a refusal and not a fault in Cerberus. Coding it is also what routes a
	// plugin's own text through Error's redaction.
	CodeOperationFailed ErrorCode = "operation_failed"
	// CodeAuditUnavailable is an operation refused because its audit record
	// could not be written (Decision 8), or, for a brake lift, would not
	// count: the log's chain does not vouch for its tail (M1). Nothing ran.
	CodeAuditUnavailable ErrorCode = "audit_unavailable"
	// CodePluginChanged is a plugin refused at load because its bundle no
	// longer matches the one the operator accepted in review, or its host
	// range excludes this Cerberus. Nothing was started.
	CodePluginChanged ErrorCode = "plugin_changed"
	// CodePrincipalRefused is a connection from a user other than the
	// daemon's, or one whose peer credentials could not be read. Nothing
	// was served.
	CodePrincipalRefused ErrorCode = "principal_refused"

	// The policy refusals. Distinct, because an agent that cannot tell "you
	// may not" from "ask a human" retries the wrong thing (section 4). In
	// shadow mode the decision is recorded and none of these is returned.
	//
	// CodePolicyDenied: policy does not allow the operation.
	CodePolicyDenied ErrorCode = "policy_denied"
	// CodeApprovalRequired: the operation needs an approval nobody has
	// asked for yet.
	CodeApprovalRequired ErrorCode = "approval_required"
	// CodeApprovalPending: an approval was asked for and has not been
	// given.
	CodeApprovalPending ErrorCode = "approval_pending"
	// CodeApprovalExpired: an approval was given and has lapsed.
	CodeApprovalExpired ErrorCode = "approval_expired"
	// CodePlanStale: the approved plan no longer matches what would run
	// (I6).
	CodePlanStale ErrorCode = "plan_stale"
	// CodeEgressRefused: the operation ran, and an enforced egress rule
	// withholds its labeled output from this caller (P4-4).
	CodeEgressRefused ErrorCode = "egress_refused"
	// CodeLockdown: Cerberus is in lockdown (§12) and only plain reads run.
	CodeLockdown ErrorCode = "lockdown"
	// CodeFrozen: the target is frozen (§12).
	CodeFrozen ErrorCode = "frozen"
	// CodeSessionSuspended: the circuit breaker suspended this session
	// (§12, P5-c) until a person resets it.
	CodeSessionSuspended ErrorCode = "session_suspended"
	// CodeDeadlineExceeded: the plugin did not answer within its deadline
	// and was stopped (P5-d). Not a refusal: a non-read may have partly
	// run.
	CodeDeadlineExceeded ErrorCode = "deadline_exceeded"
	// CodeOutputTooLarge: a read's result was over the plugin's cap and is
	// withheld (P5-d).
	CodeOutputTooLarge ErrorCode = "output_too_large"
	// CodeInsufficientScope: a verified bearer token's scopes do not cover
	// the operation's effect (WP-S8).
	CodeInsufficientScope ErrorCode = "insufficient_scope"
)

// errorCodes is the whole vocabulary, for tests that hold each code intact
// through redaction.
var errorCodes = []ErrorCode{
	CodeLockdown,
	CodeFrozen,
	CodeSessionSuspended,
	CodeDeadlineExceeded,
	CodeOutputTooLarge,
	CodeInsufficientScope,
	CodeUnavailable,
	CodeCredentialMissing,
	CodeUnsupported,
	CodeInvalidArgs,
	CodeAckRequired,
	CodePreviewUnsupported,
	CodeOperationFailed,
	CodeAuditUnavailable,
	CodePluginChanged,
	CodePrincipalRefused,
	CodePolicyDenied,
	CodeApprovalRequired,
	CodeApprovalPending,
	CodeApprovalExpired,
	CodePlanStale,
	CodeEgressRefused,
}

// ErrorCodes returns the whole vocabulary, for tests on the far side of a
// surface that must hold every code.
func ErrorCodes() []ErrorCode {
	return slices.Clone(errorCodes)
}

// Error is a coded refusal (or coded failure) the gate returns.
type Error struct {
	Code      ErrorCode
	Connector string
	Operation string
	Err       error

	// Approval is approval_pending's machine-readable half: which approval,
	// until when, and the command that decides it.
	Approval *ApprovalRef
}

// Error is "<connector> <operation>: <code>: <cause>". The head is names and
// a code, which no rule runs over; the connector id and operation name come
// from the caller, so each is checked on its own. The cause is rendered: a
// Guidance cause keeps its prose, anything else gets the rules (redact.Render).
func (e *Error) Error() string { return e.RenderRedacted(nil) }

// RenderRedacted is Error in s.
func (e *Error) RenderRedacted(s *redact.Scope) string {
	head := fmt.Sprintf("%s %s: %s", redact.Text(e.Connector), redact.Text(e.Operation), e.Code)
	if e.Err == nil {
		return s.ReplaceValues(head)
	}
	return s.ReplaceValues(head) + ": " + redact.Render(s, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

// opArgs names the operation a refusal is about.
type opArgs struct {
	Connector string
	Operation string
}

// codedError is a refusal of the operation args names.
func codedError(args opArgs, code ErrorCode, err error) error {
	return &Error{
		Code:      code,
		Connector: args.Connector,
		Operation: args.Operation,
		Err:       err,
	}
}

// NewError is a coded error for an HTTP layer or lane that refuses an
// operation on its own (an unknown operation, invalid arguments, a missing
// acknowledgment), so its refusal is coded, recorded and answered exactly
// like the gate's own.
func NewError(connector, operation string, code ErrorCode, err error) error {
	return codedError(opArgs{Connector: connector, Operation: operation}, code, err)
}

// CodeOf is err's code, or "" when it carries none.
func CodeOf(err error) ErrorCode {
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

// httpStatus is the one table from an error code to the HTTP status a
// surface answers with. A test holds it to errorCodes, so a new code cannot
// fall through to 500.
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
//   - principal_refused: the caller is not the daemon's own user.
//   - policy_denied, approval_required, approval_pending, approval_expired,
//     plan_stale: policy enforcement's refusals.
//   - operation_failed: the request passed every gate and the provider, the
//     plugin or the tool behind the connector failed it. 502, so a 500 still
//     means a fault in Cerberus itself.
var httpStatus = map[ErrorCode]int{
	CodeInvalidArgs:        http.StatusBadRequest,
	CodeAckRequired:        http.StatusConflict,
	CodePreviewUnsupported: http.StatusUnprocessableEntity,
	CodeUnsupported:        http.StatusNotFound,
	CodeUnavailable:        http.StatusServiceUnavailable,
	CodeCredentialMissing:  http.StatusServiceUnavailable,
	CodeOperationFailed:    http.StatusBadGateway,
	CodeAuditUnavailable:   http.StatusServiceUnavailable,
	CodePluginChanged:      http.StatusConflict,
	CodePrincipalRefused:   http.StatusForbidden,
	CodePolicyDenied:       http.StatusForbidden,
	CodeEgressRefused:      http.StatusForbidden,
	CodeApprovalRequired:   http.StatusPreconditionRequired,
	CodeApprovalPending:    http.StatusConflict,
	CodeApprovalExpired:    http.StatusConflict,
	CodePlanStale:          http.StatusConflict,
	CodeLockdown:           http.StatusLocked,
	CodeFrozen:             http.StatusLocked,
	CodeSessionSuspended:   http.StatusLocked,
	CodeDeadlineExceeded:   http.StatusGatewayTimeout,
	CodeOutputTooLarge:     http.StatusBadGateway,
	CodeInsufficientScope:  http.StatusForbidden,
}

// HTTPStatus returns the status for err when it carries an error code, and
// fallback otherwise.
func HTTPStatus(err error, fallback int) int {
	var connErr *Error
	if errors.As(err, &connErr) {
		if status, ok := httpStatus[connErr.Code]; ok {
			return status
		}
	}
	return fallback
}

// ErrorDetails is the machine-readable half of a coded refusal, for an
// HTTP layer's error body. Without it a client can only rebuild a string,
// and every caller downstream — the console's status mapping, the CLI's
// recovery hints — loses the code the gate had in hand.
type ErrorDetails struct {
	Code      ErrorCode `json:"code,omitempty"`
	Connector string    `json:"connector,omitempty"`
	Operation string    `json:"operation,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	// Approval is approval_pending's {id, expires_at, approve_with}.
	Approval *ApprovalRef `json:"approval,omitempty"`
}

// HTTPError is what an HTTP layer answers for err: the status, the code,
// the rendered message and the machine-readable details. ok is false when
// err carries no code (the caller picks its own status). A refusal of an
// approval decision (DecideAs, RevokeAs, AckBreakGlassAs) is uncoded: map it
// with ApprovalErrorStatus.
func HTTPError(err error) (status int, code ErrorCode, message string, details ErrorDetails, ok bool) {
	var connErr *Error
	if !errors.As(err, &connErr) {
		return 0, "", "", ErrorDetails{}, false
	}
	details = ErrorDetails{Code: connErr.Code, Connector: connErr.Connector, Operation: connErr.Operation, Approval: connErr.Approval}
	if connErr.Err != nil {
		details.Detail = connErr.Err.Error()
	}
	return HTTPStatus(err, http.StatusInternalServerError), connErr.Code, err.Error(), details, true
}
