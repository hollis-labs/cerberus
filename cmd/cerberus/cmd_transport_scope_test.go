package main

import (
	"context"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// A call the CLI runs in its own process is begun as in_process with its
// own redaction scope; a context that skips inProcessContext has none and
// still renders through the regex net.
func TestInProcessCallsBeginWithAScope(t *testing.T) {
	const labeledSecret = "token=abcdef1234567890" //nolint:gosec // a test sentinel, not a credential
	ctx := inProcessContext(context.Background())
	if cerbapi.CallerSurfaceFrom(ctx) != cerbapi.SurfaceInProcess {
		t.Fatalf("surface = %s", cerbapi.CallerSurfaceFrom(ctx))
	}
	s := redact.ScopeFrom(ctx)
	if s == nil {
		t.Fatal("in-process call has no redaction scope")
	}
	s.Add("svc/key", "q7Zr2mXv9pLw")
	if got := s.Text("q7Zr2mXv9pLw"); got != redact.Marker {
		t.Fatalf("scope did not remove a registered value: %q", got)
	}
	if got := redact.ScopeFrom(context.Background()).Text(labeledSecret); got == labeledSecret || got != redact.Text(labeledSecret) {
		t.Fatalf("unmarked context rendered %q, want the regex net", got)
	}
}
