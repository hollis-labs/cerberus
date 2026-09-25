package cerbapi

import "context"

// CallerSurface is where a request entered Cerberus. It decides which inputs
// an operation accepts: a local-only input, such as an ad-hoc docker target,
// comes only from the operator's own shell.
//
// The surface is self-reported by the process that serves the request, not
// proven. It never grants anything: in-process is the default because the
// in-process CLI is the one path with no server in front of it, and every
// server marks its requests before they reach a service.
type CallerSurface string

const (
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

// CallerSurfaceFrom returns the surface ctx entered through, or in-process
// when no server marked it.
func CallerSurfaceFrom(ctx context.Context) CallerSurface {
	if surface, ok := ctx.Value(callerSurfaceKey{}).(CallerSurface); ok && surface != "" {
		return surface
	}
	return SurfaceInProcess
}
