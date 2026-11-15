package gate

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// Every code has a status, and none of them is a 500: a refusal is the
// caller's to act on, not a server fault.
func TestHTTPStatusCoversEveryCode(t *testing.T) {
	if len(httpStatus) != len(errorCodes) {
		t.Fatalf("status table has %d codes, vocabulary has %d", len(httpStatus), len(errorCodes))
	}
	for _, code := range errorCodes {
		status, ok := httpStatus[code]
		if !ok {
			t.Errorf("%s has no HTTP status", code)
			continue
		}
		if status < 400 || status == http.StatusInternalServerError {
			t.Errorf("%s maps to %d", code, status)
		}
		err := &Error{Code: code}
		if got := HTTPStatus(err, http.StatusTeapot); got != status {
			t.Errorf("%s: HTTPStatus = %d, want %d", code, got, status)
		}
	}
	if got := HTTPStatus(errors.New("plain"), http.StatusTeapot); got != http.StatusTeapot {
		t.Errorf("uncoded error: got %d, want the fallback", got)
	}
}

// HTTPError hands an HTTP layer the status, code, text and machine-readable
// details of a coded refusal, approval_pending's reference included; an
// uncoded error is not its to answer.
func TestHTTPErrorCarriesTheCodeAndTheApproval(t *testing.T) {
	a := approval.Approval{ID: "apr_0123456789ab", Channel: approval.ChannelTTYConfirm, ExpiresAt: time.Date(2026, 9, 26, 13, 0, 0, 0, time.UTC)}
	err := approvalPendingError(opArgs{Connector: "local", Operation: "remove"}, a)
	status, code, msg, details, ok := HTTPError(err)
	if !ok || status != http.StatusConflict || code != CodeApprovalPending || msg != err.Error() {
		t.Fatalf("%d %s %q %v", status, code, msg, ok)
	}
	if details.Approval == nil || details.Approval.ID != a.ID || details.Approval.ApproveWith != "cerberus approvals approve "+a.ID ||
		!details.Approval.ExpiresAt.Equal(a.ExpiresAt) || details.Connector != "local" || details.Operation != "remove" || details.Detail == "" {
		t.Fatalf("details %+v", details)
	}
	if got := redact.Text(msg); got != msg {
		t.Fatalf("redaction changed the refusal:\n  %s\n  %s", msg, got)
	}
	if _, _, _, _, ok := HTTPError(errors.New("plain")); ok {
		t.Fatal("an uncoded error was answered")
	}
}
