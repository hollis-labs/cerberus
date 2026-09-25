package cerbapi

import "context"

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
)

type callerSurfaceKey struct{}

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
