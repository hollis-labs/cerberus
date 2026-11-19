# MCP library adoption

CW-20261119-0141 makes the existing MCP HTTP OAuth verifier implement
`go-mcp/auth.Provider` and applies `auth.HTTPMiddleware`. Token validation,
resource metadata URLs, one-minute clock skew, scope checks and token
forwarding to the daemon retain their existing behavior. Stdio continues to
use the local socket identity path.

All production MCP server constructors already use `go-mcp/server` through
`internal/mcp.NewServer`; HTTP uses `go-mcp/transport/http.NewHandler`.
There are no direct SDK server or Streamable HTTP handler constructors in
this repository. SDK request/result types, receiving middleware and protected
resource metadata rendering remain valid integration points. Metadata rendering
stays as-is to preserve its public response and method behavior.

The pinned module already provides the auth provider seam. Upgrading the
module's Host/Origin defaults is a separate behavior change: the existing
Cerberus guard derives allowed authorities from configuration and certificates.
