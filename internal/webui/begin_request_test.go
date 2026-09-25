package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

const labeledSecret = "token=abcdef1234567890" //nolint:gosec // a test sentinel, not a credential

// Every console request is begun as the web surface with its own redaction
// scope; a handler reached without markWebSurface has no scope and still
// renders through the regex net.
func TestConsoleRequestsBeginWithAScope(t *testing.T) {
	var seen context.Context
	capture := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.Context() })

	markWebSurface(capture).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/session", nil))
	if cerbapi.CallerSurfaceFrom(seen) != cerbapi.SurfaceWeb {
		t.Fatalf("surface = %s", cerbapi.CallerSurfaceFrom(seen))
	}
	s := redact.ScopeFrom(seen)
	if s == nil {
		t.Fatal("console request has no redaction scope")
	}
	s.Add("svc/key", "q7Zr2mXv9pLw")
	if got := s.Text("q7Zr2mXv9pLw"); got != redact.Marker {
		t.Fatalf("scope did not remove a registered value: %q", got)
	}

	capture.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/session", nil))
	if redact.ScopeFrom(seen) != nil {
		t.Fatal("unwrapped handler has a scope")
	}
	if got := redact.ScopeFrom(seen).Text(labeledSecret); got == labeledSecret || got != redact.Text(labeledSecret) {
		t.Fatalf("unwrapped handler rendered %q, want the regex net", got)
	}
}

// A credential registered on a console request is gone from the error body
// the console writes for it.
func TestConsoleResponsesRenderThroughTheRequestScope(t *testing.T) {
	const sentinel = "q7Zr2mXv9pLw" //nolint:gosec // a test sentinel, not a credential
	rec := httptest.NewRecorder()
	markWebSurface(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redact.ScopeFrom(r.Context()).Add("vercel/token", sentinel)
		writeError(w, http.StatusBadGateway, "deploy step printed "+sentinel)
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/deployments/site/run", nil))
	if body := rec.Body.String(); strings.Contains(body, sentinel) || !strings.Contains(body, redact.Marker) {
		t.Fatalf("console body = %s", body)
	}
}
