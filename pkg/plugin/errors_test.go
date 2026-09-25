package plugin

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorResultRoundTrips(t *testing.T) {
	result := ErrorResult(ErrorUnavailable, "cannot reach the gateway")
	if !result.IsError {
		t.Fatal("ErrorResult is not marked IsError")
	}
	code, message, ok := ParseErrorResult(result.Content)
	if !ok || code != ErrorUnavailable || message != "cannot reach the gateway" {
		t.Fatalf("ParseErrorResult = %q, %q, %v", code, message, ok)
	}
}

func TestResultForErrorFindsACodeAnywhereInTheChain(t *testing.T) {
	base := WithCode(ErrorUnavailable, errors.New("connection refused"))
	wrapped := fmt.Errorf("get health: %w", base)
	result, ok := ResultForError(wrapped)
	if !ok {
		t.Fatal("a wrapped CodedError was not found")
	}
	code, message, _ := ParseErrorResult(result.Content)
	if code != ErrorUnavailable || message != "get health: connection refused" {
		t.Fatalf("got %q, %q; want the code and the whole message", code, message)
	}
	if _, ok := ResultForError(errors.New("plain")); ok {
		t.Fatal("an uncoded error produced a coded result")
	}
	if WithCode(ErrorUnavailable, nil) != nil {
		t.Fatal("WithCode(nil) must stay nil")
	}
}

// A tool's own error JSON is not mistaken for a coded failure.
func TestParseErrorResultIgnoresOtherContent(t *testing.T) {
	for _, content := range []string{`{"error":"nope"}`, `"just text"`, `not json`, `{"cerberus_error":{}}`} {
		if _, _, ok := ParseErrorResult([]byte(content)); ok {
			t.Errorf("ParseErrorResult(%s) reported a coded failure", content)
		}
	}
}

func TestErrorCodeVocabulary(t *testing.T) {
	for _, code := range ErrorCodes {
		if !code.Valid() {
			t.Errorf("%s is not Valid", code)
		}
	}
	for _, gate := range []ErrorCode{"acknowledgment_required", "preview_unsupported", "operation_unsupported", ""} {
		if gate.Valid() {
			t.Errorf("%q must not be a plugin code: it belongs to the host's gate", gate)
		}
	}
}
