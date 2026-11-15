package api

import (
	"context"
	"net/http"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/gate"
)

// ViaAPI is the channel of a caller on this HTTP API with a machine credential: an API key, a
// token minted at /api/token, or the auth module's off-mode operator. The old build had no such
// surface (its callers came over the socket, or mcp-http with a bearer token).
const ViaAPI = "api"

// LocalTrustID is the principal Substrate's MCP stdio stamps under LocalTrust.
const LocalTrustID = "local:operator"

// GatePrincipal maps the caller the auth guard established onto the operation gate's
// principal, and the surface its request entered through. The gate's principal is a label for
// default policy, never approval; approving is decided by IsConsolePerson and the owner
// session, not by this label.
//
//	console person (IsConsolePerson)   human via web, session = console session id    SurfaceWeb
//	local trust (MCP stdio)            agent via mcp_stdio, client = session = its id SurfaceSocket
//	API key, any minted token, off     agent via api, client = session = principal id SurfaceSocket
//	no principal                       agent via unknown, self-reported               SurfaceSocket
//
// A token minted from the owner's session is an agent: it is a bearer credential any process
// holding it can send, which is why it cannot approve either. The principal id is the caller's
// session as well as its client: approval.SameRequester compares kind, channel and session, so
// an approval or grant one key asked for is not usable by another key (the old build told
// mcp-http callers apart by their verified token subject; these principals carry no subject
// the gate's oauth scope check would then apply to). A token minted from a key names the key,
// so it is that key's caller, across refreshes.
func GatePrincipal(p libauth.Principal, ok bool) (gate.CallerSurface, gate.Principal) {
	switch {
	case !ok:
		return gate.SurfaceSocket, gate.Principal{Kind: gate.PrincipalAgent, Via: gate.ViaUnknown, UID: -1, SelfReported: true}
	case IsConsolePerson(p):
		return gate.SurfaceWeb, gate.WebSessionPrincipal(p.SessionID)
	case p.ID == LocalTrustID:
		return gate.SurfaceSocket, gate.Principal{Kind: gate.PrincipalAgent, Via: gate.ViaMCPStdio, UID: -1, Client: p.ID, Session: p.ID, SelfReported: !p.Verified}
	}
	return gate.SurfaceSocket, gate.Principal{Kind: gate.PrincipalAgent, Via: ViaAPI, UID: -1, Client: p.ID, Session: p.ID, SelfReported: !p.Verified}
}

// GateContext is ctx entered into the gate (gate.BeginRequest) as the caller the auth guard
// put on it: the surface, the principal and a request-scoped redactor.
func GateContext(ctx context.Context) context.Context {
	surface, who := GatePrincipal(libauth.FromContext(ctx))
	return gate.WithPrincipal(gate.BeginRequest(ctx, surface), who)
}

// principalFrom is the caller the auth guard put on r.
func principalFrom(r *http.Request) (libauth.Principal, bool) {
	return libauth.FromContext(r.Context())
}
