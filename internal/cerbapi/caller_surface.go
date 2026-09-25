package cerbapi

import (
	"context"
	"net/http"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// CallerSurface is where a request entered Cerberus. It decides which inputs
// an operation accepts: a local-only input, such as an ad-hoc docker target,
// comes only from the operator's own shell.
//
// The surface is self-reported by the process that serves the request, not
// proven. It fails closed: an unmarked context is SurfaceUnknown and is
// treated as remote, so a caller that reaches a service without marking —
// a new handler, a scheduler, a server that forgets — never gets the
// operator's shell's power. The in-process CLI marks itself, at the one place
// it hands out a local service (newLocalConnectorExecutor in cmd/cerberus).
type CallerSurface string

const (
	// SurfaceUnknown is an unmarked context. It is treated as remote.
	SurfaceUnknown CallerSurface = "unknown"
	// SurfaceInProcess is the CLI running the service in its own process.
	SurfaceInProcess CallerSurface = "in_process"
	// SurfaceSocket is the daemon socket: the CLI through the daemon, and
	// `cerberus mcp`.
	SurfaceSocket CallerSurface = "socket"
	// SurfaceWeb is the web console.
	SurfaceWeb CallerSurface = "web"
	// SurfaceMonitor is the resource monitor: Cerberus restarting a
	// crashed workload on its own. It is recorded as an automation
	// principal and is never gated (Decision 14).
	SurfaceMonitor CallerSurface = "monitor"
)

type callerSurfaceKey struct{}

// BeginRequest is where a request enters Cerberus: it marks ctx with the
// surface the request came through, the principal it is from, and a
// request-scoped redactor (redact.Scope), which credentials resolved on the
// request register with and which its error and log paths render through.
// Every entry point calls this rather than WithCallerSurface, so an entry
// point cannot mark a surface and forget the scope or the principal — they
// start together.
//
// The principal is a label for default policy, never approval; see
// Principal.
//
// A ctx that already carries a scope keeps it. A path that never reaches
// BeginRequest has no scope, and redact.ScopeFrom returns nil, which renders
// as the regex net alone: forgetting it costs value redaction, never
// redaction.
func BeginRequest(ctx context.Context, surface CallerSurface) context.Context {
	ctx = WithCallerSurface(ctx, surface)
	ctx = WithPrincipal(ctx, requestPrincipal(ctx, surface))
	ctx, _ = redact.EnsureScope(ctx)
	return ctx
}

// BeginHTTPRequest is BeginRequest for an HTTP server. It also hands the
// request's scope to the response writer, because the JSON writers every
// handler answers through take a ResponseWriter and no request: through
// ResponseScope they render in the request's scope without each of a few
// hundred call sites passing it along, and without one being able to forget.
//
// Only the socket reads a caller's claim about itself (PrincipalHeader),
// because only the socket knows the caller's uid. The web console reads
// nothing the browser sends as a claim.
func BeginHTTPRequest(w http.ResponseWriter, r *http.Request, surface CallerSurface) (http.ResponseWriter, *http.Request) {
	ctx := r.Context()
	if surface == SurfaceSocket {
		if claim, ok := principalFromHeader(r.Header); ok {
			ctx = WithPrincipal(ctx, claim)
		}
	}
	r = r.WithContext(BeginRequest(ctx, surface))
	return scopedResponseWriter{ResponseWriter: w, scope: redact.ScopeFrom(r.Context())}, r
}

// ResponseScope is the redaction scope of the request w answers, or nil —
// the regex net alone — for a writer BeginHTTPRequest did not produce.
func ResponseScope(w http.ResponseWriter) *redact.Scope {
	if sw, ok := w.(scopedResponseWriter); ok {
		return sw.scope
	}
	return nil
}

type scopedResponseWriter struct {
	http.ResponseWriter
	scope *redact.Scope
}

// Flush keeps the progress stream working through the wrapper.
func (w scopedResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w scopedResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WithCallerSurface marks ctx as having entered through surface. A server
// sets it once, at the edge, for every request it serves.
func WithCallerSurface(ctx context.Context, surface CallerSurface) context.Context {
	return context.WithValue(ctx, callerSurfaceKey{}, surface)
}

// CallerSurfaceFrom returns the surface ctx entered through, or
// SurfaceUnknown when nothing marked it.
func CallerSurfaceFrom(ctx context.Context) CallerSurface {
	if surface, ok := ctx.Value(callerSurfaceKey{}).(CallerSurface); ok && surface != "" {
		return surface
	}
	return SurfaceUnknown
}
