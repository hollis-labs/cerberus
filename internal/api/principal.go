package api

import (
	"context"
	"net/http"
	"strings"
	"unicode"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/gate"
)

// ViaAPI is the channel of a caller on this HTTP API with a machine credential: an API key, a
// token minted at /api/token, or the auth module's off-mode operator. The old build had no such
// surface (its callers came over the socket, or mcp-http with a bearer token).
const ViaAPI = "api"

// LocalTrustID is the principal Substrate's MCP stdio stamps under LocalTrust.
const LocalTrustID = "local:operator"

// Key roles: the owner names a key at creation (offline, `cerberus identity key create --name`),
// and the name of an API key (not of a token minted from it) says which client it is, as the
// old socket told the CLI and `cerberus mcp` apart (plan section 6, key list at D6).
const (
	// CLIKeyName is the CLI's key (cerberus.cli_key_file): its calls are via cli.
	CLIKeyName = "cli"
	// MCPKeyName is `cerberus mcp`'s key (cerberus.mcp_key_file): its calls are via mcp_stdio,
	// labeled with the MCP client the forwarder names in MCPClientHeader.
	MCPKeyName = "cerberus-mcp"
)

// MCPClientHeader carries the MCP client's clientInfo name from `cerberus mcp` to the serving
// Cerberus. It is read only on a call made with the MCPKeyName key, and it is a label: unverified,
// as the old build's stdio clientInfo was, so a policy rule naming a client matches as before.
const MCPClientHeader = "X-Cerberus-Mcp-Client"

// MCPCaller is an MCP tool call's channel and client label, for the gate.
type MCPCaller struct {
	Via    string // gate.ViaMCPStdio or gate.ViaMCPHTTP
	Client string // the MCP clientInfo name; unverified
}

type mcpCallerKey struct{}

// WithMCPCaller marks ctx as an MCP tool call. HTTP /mcp, served in-process, stamps it with
// gate.ViaMCPHTTP; it is a context value, so no network caller can set it.
func WithMCPCaller(ctx context.Context, c MCPCaller) context.Context {
	return context.WithValue(ctx, mcpCallerKey{}, c)
}

func mcpCallerFrom(ctx context.Context) (MCPCaller, bool) {
	c, ok := ctx.Value(mcpCallerKey{}).(MCPCaller)
	return c, ok
}

// keyNamed reports whether p is the API key named name itself, not a token minted from it: the
// identity module verifies a key as ID "key:<id>" with its own id as TokenID and no Parent, while a
// minted token names its key as Parent.
func keyNamed(p libauth.Principal, name string) bool {
	id, isKey := strings.CutPrefix(p.ID, "key:")
	return isKey && p.Parent == "" && (p.TokenID == "" || p.TokenID == id) && p.Display == name
}

// clientLabel is an MCP client name as the gate records it: printable, bounded, and the old
// build's "mcp-client" when the client named none.
func clientLabel(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, strings.TrimSpace(name))
	if len(name) > 128 {
		name = name[:128]
	}
	if name == "" {
		return "mcp-client"
	}
	return name
}

// stampMCPStdio marks a call made with the MCPKeyName key as `cerberus mcp`'s, with the client
// the forwarder names. A call already marked (HTTP /mcp in-process) keeps its mark; the header
// from any other caller is ignored.
func stampMCPStdio(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, marked := mcpCallerFrom(r.Context()); !marked {
			if p, ok := principalFrom(r); ok && keyNamed(p, MCPKeyName) {
				r = r.WithContext(WithMCPCaller(r.Context(), MCPCaller{Via: gate.ViaMCPStdio, Client: r.Header.Get(MCPClientHeader)}))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// GatePrincipal maps the caller the auth guard established onto the operation gate's
// principal, and the surface its request entered through. The gate's principal is a label for
// default policy, never approval; approving is decided by IsConsolePerson and the owner
// session, not by this label.
//
//	console person (IsConsolePerson)   human via web, session = console session id    SurfaceWeb
//	local trust (MCP stdio)            agent via mcp_stdio, client = session = its id SurfaceSocket
//	the CLI's key (CLIKeyName)         agent via cli, client cerberus-cli, session = key id
//	API key, any minted token, off     agent via api, client = session = principal id SurfaceSocket
//
// An MCP tool call (MCPCaller on the context: HTTP /mcp, or the MCPKeyName key) is an agent via
// mcp_http or mcp_stdio whoever sent it, with the MCP client as its client and the principal id
// as its session (GateContext): the old build made every mcp-http caller an agent, and keyed its
// breaker by channel and session, so MCP callers never share the CLI's.
//
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
	case keyNamed(p, CLIKeyName):
		return gate.SurfaceSocket, gate.Principal{Kind: gate.PrincipalAgent, Via: gate.ViaCLI, UID: -1, Client: "cerberus-cli", Session: p.ID, SelfReported: !p.Verified}
	}
	return gate.SurfaceSocket, gate.Principal{Kind: gate.PrincipalAgent, Via: ViaAPI, UID: -1, Client: p.ID, Session: p.ID, SelfReported: !p.Verified}
}

// GateContext is ctx entered into the gate (gate.BeginRequest) as the caller the auth guard
// put on it: the surface, the principal and a request-scoped redactor.
func GateContext(ctx context.Context) context.Context {
	p, ok := libauth.FromContext(ctx)
	surface, who := GatePrincipal(p, ok)
	if c, isMCP := mcpCallerFrom(ctx); isMCP && ok {
		surface, who = gate.SurfaceSocket, gate.Principal{Kind: gate.PrincipalAgent, Via: c.Via, UID: -1,
			Client: clientLabel(c.Client), Session: p.ID, SelfReported: !p.Verified}
	}
	return gate.WithPrincipal(gate.BeginRequest(ctx, surface), who)
}

// principalFrom is the caller the auth guard put on r.
func principalFrom(r *http.Request) (libauth.Principal, bool) {
	return libauth.FromContext(r.Context())
}
