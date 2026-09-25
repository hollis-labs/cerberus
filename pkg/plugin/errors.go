package plugin

import (
	"encoding/json"
	"errors"

	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// ErrorCode is a Cerberus connector error code a plugin attaches to a failed
// operation. The host reports it as the operation's code on every surface,
// and it is what a caller, an agent most of all, branches on. A failure a
// plugin leaves uncoded is reported as operation_failed, or as
// credential_missing when the plugin loaded without a credential it declares.
//
// The vocabulary is the subset of the host's codes that describe what went
// wrong upstream. The gate's own codes (acknowledgment_required,
// preview_unsupported, operation_unsupported) belong to the host and are not
// offered: a plugin never needs to claim them, because the host refuses
// before calling it.
type ErrorCode string

const (
	// ErrorUnavailable: the system the plugin talks to cannot be reached,
	// such as a refused connection, a down tunnel or an unreachable API
	// server. The request may succeed once that is fixed.
	ErrorUnavailable ErrorCode = "connector_unavailable"
	// ErrorCredentialMissing: the plugin has no usable credential for this
	// call, because none was supplied or the provider rejected it as absent.
	ErrorCredentialMissing ErrorCode = "credential_missing" //nolint:gosec // an error code name, not a credential
	// ErrorInvalidArgs: the caller's arguments are wrong.
	ErrorInvalidArgs ErrorCode = "invalid_args"
	// ErrorOperationFailed: the operation ran and failed for another reason.
	ErrorOperationFailed ErrorCode = "operation_failed"
)

// ErrorCodes is the whole vocabulary a plugin may use.
var ErrorCodes = []ErrorCode{ErrorUnavailable, ErrorCredentialMissing, ErrorInvalidArgs, ErrorOperationFailed}

// Valid reports whether c is one of ErrorCodes.
func (c ErrorCode) Valid() bool {
	for _, known := range ErrorCodes {
		if c == known {
			return true
		}
	}
	return false
}

// CodedError is an error carrying the code the plugin wants the host to
// report. Return one from a backend and turn it into a tool result with
// ResultForError.
type CodedError struct {
	Code ErrorCode
	Err  error
}

func (e *CodedError) Error() string {
	if e.Err == nil {
		return string(e.Code)
	}
	return e.Err.Error()
}

func (e *CodedError) Unwrap() error { return e.Err }

// WithCode attaches code to err. A nil err stays nil.
func WithCode(code ErrorCode, err error) error {
	if err == nil {
		return nil
	}
	return &CodedError{Code: code, Err: err}
}

// errorPayload is the wire shape of a coded failure: an MCP tool error result
// whose content is this object. It is namespaced so a tool that returns its
// own error JSON is never mistaken for one.
type errorPayload struct {
	Error struct {
		Code    ErrorCode `json:"code"`
		Message string    `json:"message"`
	} `json:"cerberus_error"`
}

// ErrorResult is the tool result that reports a failure with a code. A plugin
// returns it from MCPCallTool with a nil error.
func ErrorResult(code ErrorCode, message string) subprocess.MCPCallResult {
	var payload errorPayload
	payload.Error.Code = code
	payload.Error.Message = message
	content, _ := json.Marshal(payload) // a struct of two strings always marshals
	return subprocess.MCPCallResult{Content: content, IsError: true}
}

// ResultForError converts err into a coded tool result when anything in its
// chain is a CodedError. It reports false for an uncoded error, which the
// plugin returns as an ordinary error.
func ResultForError(err error) (subprocess.MCPCallResult, bool) {
	var coded *CodedError
	if !errors.As(err, &coded) {
		return subprocess.MCPCallResult{}, false
	}
	return ErrorResult(coded.Code, err.Error()), true
}

// ParseErrorResult reads a coded failure out of a tool error result's
// content. It is the host's half of ErrorResult. It reports false for content
// that is not a coded failure; an unknown code is returned as-is, and the
// host decides what to do with it.
func ParseErrorResult(content []byte) (ErrorCode, string, bool) {
	var payload errorPayload
	if err := json.Unmarshal(content, &payload); err != nil || payload.Error.Code == "" {
		return "", "", false
	}
	return payload.Error.Code, payload.Error.Message, true
}
