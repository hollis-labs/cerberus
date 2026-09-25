package cerbapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// scopeSentinel carries no label, so only a value registered with a scope
// removes it; labeledSecret is what the regex net removes on its own.
const (
	scopeSentinel = "q7Zr2mXv9pLw"           //nolint:gosec // a test sentinel, not a credential
	labeledSecret = "token=abcdef1234567890" //nolint:gosec // a test sentinel, not a credential
)

// assertScoped checks that s is a begun request's scope: its scope removes a
// value registered on it.
func assertScoped(t *testing.T, s *redact.Scope) {
	t.Helper()
	if s == nil {
		t.Fatal("request has no redaction scope")
	}
	s.Add("svc/key", scopeSentinel)
	if got := s.Text("vendor said: " + scopeSentinel); got != "vendor said: "+redact.Marker {
		t.Fatalf("scope did not remove a registered value: %q", got)
	}
}

// assertRegexNetOnly checks the fallback every entry point degrades to when
// it is bypassed: no scope, and rendering through ScopeFrom is still the
// regex net — never no redaction.
func assertRegexNetOnly(t *testing.T, s *redact.Scope) {
	t.Helper()
	if s != nil {
		t.Fatalf("unwrapped path has a scope: %s", s)
	}
	if got := s.Text(labeledSecret); got != redact.Text(labeledSecret) || got == labeledSecret {
		t.Fatalf("nil scope rendered %q, want the regex net's %q", got, redact.Text(labeledSecret))
	}
}

func TestBeginRequestMarksTheSurfaceAndGivesAScope(t *testing.T) {
	for _, surface := range []CallerSurface{SurfaceInProcess, SurfaceSocket, SurfaceWeb, SurfaceMonitor} {
		ctx := BeginRequest(context.Background(), surface)
		if got := CallerSurfaceFrom(ctx); got != surface {
			t.Errorf("surface = %s, want %s", got, surface)
		}
		assertScoped(t, redact.ScopeFrom(ctx))
	}
	assertRegexNetOnly(t, redact.ScopeFrom(WithCallerSurface(context.Background(), SurfaceSocket)))
}

// A request has one scope. An entry point reached from inside a begun
// request joins its scope, so a value registered deeper in is removed by the
// outer render edges too.
func TestBeginRequestJoinsTheScopeItIsAlreadyIn(t *testing.T) {
	outer := BeginRequest(context.Background(), SurfaceWeb)
	inner := BeginRequest(outer, SurfaceInProcess)
	if redact.ScopeFrom(inner) != redact.ScopeFrom(outer) {
		t.Fatal("a nested BeginRequest started a second scope")
	}
	redact.ScopeFrom(inner).Add("svc/key", scopeSentinel)
	if got := redact.ScopeFrom(outer).Text(scopeSentinel); got != redact.Marker {
		t.Fatalf("outer scope missed a value registered inside: %q", got)
	}
}

func TestSocketServerBeginsEveryRequest(t *testing.T) {
	var seen context.Context
	capture := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.Context() })
	s := &SocketServer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	s.wrap(capture).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if CallerSurfaceFrom(seen) != SurfaceSocket {
		t.Fatalf("surface = %s", CallerSurfaceFrom(seen))
	}
	assertScoped(t, redact.ScopeFrom(seen))

	capture.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	assertRegexNetOnly(t, redact.ScopeFrom(seen))
}

func TestMonitorChecksBeginARequest(t *testing.T) {
	ctx := monitorCheckContext(context.Background())
	if CallerSurfaceFrom(ctx) != SurfaceMonitor {
		t.Fatalf("surface = %s", CallerSurfaceFrom(ctx))
	}
	assertScoped(t, redact.ScopeFrom(ctx))
	// Each check gets its own scope: one resource's credentials are not
	// the next one's to remove, and a scope does not grow for the
	// daemon's lifetime.
	if redact.ScopeFrom(monitorCheckContext(context.Background())) == redact.ScopeFrom(ctx) {
		t.Fatal("two checks share a scope")
	}
	assertRegexNetOnly(t, redact.ScopeFrom(context.Background()))
}
