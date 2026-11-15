package gate

import (
	"context"
	"fmt"
	"os"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// A Principal is who a request is from, as well as Cerberus can tell
// (docs/plans/live-systems-security-target.md, section 3).
//
// It is a label that chooses default policy. It is never approval, and
// nothing may treat it as approval: an agent with a shell can run the CLI as
// the user, and nothing local can prove a call came from a human. The kind,
// the channel and the client are what the caller says about itself. Only the
// uid is established by the kernel, and only where UIDVerified says so.
// Approval is the control, and it always goes out of band (I5).
//
// The HTTP layer maps its own authenticated principal onto this one and
// attaches it with WithPrincipal; the gate never reads a credential.
type Principal struct {
	Kind PrincipalKind `json:"kind"`
	// Via is the section 3 surface: who is on the other end — the CLI, an
	// MCP client, the web console, the monitor, a pipeline. CallerSurface is
	// the transport the request entered this process through.
	Via string `json:"via"`
	// UID is the caller's user id. UIDVerified is true when the kernel said
	// so. -1 is unknown.
	UID         int  `json:"uid"`
	UIDVerified bool `json:"uid_verified"`
	// Client is the MCP clientInfo name/version, or a declared label.
	Client     string `json:"client,omitempty"`
	Session    string `json:"session,omitempty"`
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
	// ActingFor is the kind of caller automation acts for: a pipeline run's
	// initiator. Set by Cerberus, never from a claim; policy treats
	// automation acting for an agent as that agent (B2).
	ActingFor PrincipalKind `json:"acting_for,omitempty"`
	// SelfReported marks Kind, Via and Client as the caller's own claim.
	SelfReported bool `json:"self_reported"`

	// A caller whose bearer token the daemon verified (WP-S8): who the
	// token names, which issuer vouched for it, and what it may ask for.
	// Set only by the daemon, never from a claim.
	Subject    string   `json:"subject,omitempty"`
	Issuer     string   `json:"issuer,omitempty"`
	AuthMethod string   `json:"auth_method,omitempty"`
	TokenID    string   `json:"token_id,omitempty"`
	Scopes     []string `json:"scopes,omitempty"`
}

// Verified reports whether the caller's identity is a verified token's.
func (p Principal) Verified() bool { return p.AuthMethod == AuthOAuth && !p.SelfReported }

// AuthOAuth is a principal established by a verified bearer token.
const AuthOAuth = "oauth"

// PrincipalKind is the coarse class of a principal.
type PrincipalKind string

const (
	PrincipalHuman      PrincipalKind = "human"
	PrincipalAgent      PrincipalKind = "agent"
	PrincipalAutomation PrincipalKind = "automation"
)

// Channels a principal arrives through (Principal.Via).
const (
	ViaCLI      = "cli"
	ViaMCPStdio = "mcp_stdio"
	ViaMCPHTTP  = "mcp_http"
	ViaWeb      = "web"
	ViaMonitor  = "monitor"
	ViaPipeline = "pipeline"
	ViaUnknown  = "unknown"
)

type principalKey struct{}

// WithPrincipal marks ctx with p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal ctx carries.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// requestPrincipal is the principal BeginRequest gives a request that entered
// through surface. A claim already on ctx is kept. Without one, the strict
// reading applies: an unknown caller is an agent.
//
// The old build classified an unclaimed in-process CLI by its terminals and
// a socket caller by its peer credentials; neither surface is the gate's to
// serve now, so an unclaimed in-process caller is an agent at the CLI (the
// strict reading of an unclassified CLI) with this process's uid, and a
// socket caller's uid is unknown.
func requestPrincipal(ctx context.Context, surface CallerSurface) Principal {
	claim, claimed := PrincipalFrom(ctx)
	switch surface {
	case SurfaceInProcess:
		if !claimed {
			claim = Principal{Kind: PrincipalAgent, Via: ViaCLI, Client: "cerberus-cli", SelfReported: true}
		}
		claim.UID, claim.UIDVerified = os.Getuid(), true
		return claim
	case SurfaceSocket:
		if !claimed {
			claim = Principal{Kind: PrincipalAgent, Via: ViaUnknown}
		}
		claim.SelfReported = true
		claim.UID, claim.UIDVerified = -1, false
		return claim
	case SurfaceWeb:
		// The label every console request starts with. Nothing the browser
		// sends is read as a claim. Only a signed-in session reaches an API
		// route, and the console then replaces this with that session's
		// principal (WebSessionPrincipal): an HTTP layer attaches it with
		// WithPrincipal after BeginRequest.
		return Principal{Kind: PrincipalHuman, Via: ViaWeb, UID: -1, SelfReported: true}
	case SurfaceMonitor:
		return Principal{Kind: PrincipalAutomation, Via: ViaMonitor, UID: os.Getuid(), UIDVerified: true, Client: "resource-monitor"}
	case SurfaceScheduler:
		return Principal{Kind: PrincipalAutomation, ActingFor: PrincipalAgent, Via: "scheduler", UID: -1, Client: "scheduler"}
	case SurfaceUnknown:
	}
	return Principal{Kind: PrincipalAgent, Via: ViaUnknown, UID: -1, SelfReported: true}
}

// WebSessionPrincipal is the principal of a console request made by a
// signed-in session (P2-2, Decision 20): a human, established by sign-in
// rather than claimed, named by the session's public id. The uid stays
// unverified.
func WebSessionPrincipal(session string) Principal {
	return Principal{Kind: PrincipalHuman, Via: ViaWeb, UID: -1, Session: session}
}

// PipelinePrincipal is the principal a pipeline's stages run as: automation
// acting for whoever started the run.
func PipelinePrincipal(ctx context.Context, id string) Principal {
	p := Principal{Kind: PrincipalAutomation, Via: ViaPipeline, UID: os.Getuid(), UIDVerified: true, Client: "pipeline:" + id}
	if caller, ok := PrincipalFrom(ctx); ok {
		p.OnBehalfOf = fmt.Sprintf("%s via %s", caller.Kind, caller.Via)
		p.ActingFor = caller.Kind
		if caller.Kind == PrincipalAutomation {
			p.ActingFor = caller.ActingFor
		}
		if caller.Client != "" {
			p.OnBehalfOf += " (" + caller.Client + ")"
		}
	}
	return p
}

// CallerSurface is where a request entered Cerberus. It decides which inputs
// an operation accepts: a local-only input, such as an ad-hoc docker target,
// comes only from the operator's own shell.
//
// The surface is self-reported by the process that serves the request, not
// proven. It fails closed: an unmarked context is SurfaceUnknown and is
// treated as remote, so a caller that reaches the gate without marking
// never gets the operator's shell's power.
type CallerSurface string

const (
	// SurfaceUnknown is an unmarked context. It is treated as remote.
	SurfaceUnknown CallerSurface = "unknown"
	// SurfaceInProcess is the CLI running the gate in its own process.
	SurfaceInProcess CallerSurface = "in_process"
	// SurfaceSocket is the daemon's local API: the CLI through the daemon,
	// and `cerberus mcp`.
	SurfaceSocket CallerSurface = "socket"
	// SurfaceWeb is the web console.
	SurfaceWeb CallerSurface = "web"
	// SurfaceMonitor is the resource monitor: Cerberus restarting a
	// crashed workload on its own. It is recorded as an automation
	// principal and is never gated (Decision 14).
	SurfaceMonitor CallerSurface = "monitor"
	// SurfaceScheduler is unattended job dispatch, always gated. It is never
	// the resource monitor's privileged supervision lane.
	SurfaceScheduler CallerSurface = "scheduler"
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
// Principal. An HTTP layer that has mapped its authenticated caller attaches
// it with WithPrincipal after BeginRequest, which replaces the surface's
// default label.
//
// A ctx that already carries a scope keeps it.
func BeginRequest(ctx context.Context, surface CallerSurface) context.Context {
	ctx = WithCallerSurface(ctx, surface)
	ctx = WithPrincipal(ctx, requestPrincipal(ctx, surface))
	ctx, _ = redact.EnsureScope(ctx)
	return ctx
}

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
