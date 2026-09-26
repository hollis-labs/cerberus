// Package loopback holds the guard shared by Cerberus's loopback HTTP
// surfaces (`cerberus web` and `cerberus mcp-http`). Neither surface
// authenticates its caller yet, so both rely on being reachable only from this
// machine. That takes two checks:
//
//   - CheckListen refuses a bind that is not loopback, so one flag cannot move
//     an unauthenticated endpoint onto a network.
//   - Guard rejects any request whose Host is not a loopback name, before any
//     handler runs. That is what defeats DNS rebinding. A page on evil.example
//     whose name has been re-pointed at 127.0.0.1 reaches the socket, but its
//     requests carry Host: evil.example.
package loopback

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// loopbackNames are the hostnames accepted in a Host header regardless of the
// configured listen host.
var loopbackNames = []string{"localhost", "127.0.0.1", "::1"}

// CheckListen returns an error unless addr binds only loopback. The host must
// be `localhost` or a literal loopback IP (127.0.0.0/8 or ::1). Any other name
// is refused rather than resolved: resolving it here and again at net.Listen
// is two lookups that can disagree, and the guard would then trust that name
// as a Host, putting whatever name an operator is talked into on the
// allow-list. An empty host (":4783") binds every interface and is refused.
// surface names the command in the error, e.g. "cerberus web".
func CheckListen(surface, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s: invalid --listen %q: %w", surface, addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil && host != "" {
		return fmt.Errorf("%s: refusing to listen on %s: --listen takes localhost, 127.0.0.1 or [::1] (or another literal loopback IP), not a hostname", surface, addr)
	}
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s: refusing to listen on %s: not a loopback address, and this surface has no authentication yet; use localhost, 127.0.0.1 or [::1]", surface, addr)
	}
	return nil
}

// Guard enforces the Host and Origin allow-lists for one listening surface.
type Guard struct {
	hosts   map[string]struct{}
	origins map[string]struct{}
	port    string
}

// NewGuard builds a guard for a server listening on listenHost:port. port is
// the port actually bound. extraOrigins are exact Origin values the operator
// allowed on top of the loopback set, such as mcp-http's --allow-origin.
func NewGuard(listenHost, port string, extraOrigins ...string) *Guard {
	g := &Guard{hosts: map[string]struct{}{}, origins: map[string]struct{}{}, port: port}
	names := append([]string{}, loopbackNames...)
	if listenHost != "" {
		names = append(names, listenHost)
	}
	for _, name := range names {
		name = strings.ToLower(strings.Trim(name, "[]"))
		g.hosts[name] = struct{}{}
		hostPort := net.JoinHostPort(name, port)
		g.origins["http://"+hostPort] = struct{}{}
		g.origins["https://"+hostPort] = struct{}{}
	}
	for _, origin := range extraOrigins {
		if origin = strings.ToLower(strings.TrimSpace(origin)); origin != "" {
			g.origins[origin] = struct{}{}
		}
	}
	return g
}

// AllowHosts adds exact host names to the allow-list, with their origins on
// the bound port. It is for a surface the operator deliberately put off
// loopback (mcp-http --insecure-listen), where clients name the machine by
// its own hostname or address: the guard still refuses every other name, so
// a rebinding page carrying its own hostname is still turned away.
func (g *Guard) AllowHosts(names ...string) {
	for _, name := range names {
		name = strings.ToLower(strings.Trim(strings.TrimSpace(name), "[]"))
		if name == "" {
			continue
		}
		g.hosts[name] = struct{}{}
		hostPort := net.JoinHostPort(name, g.port)
		g.origins["http://"+hostPort] = struct{}{}
		g.origins["https://"+hostPort] = struct{}{}
	}
}

// NewGuardForAddr builds a guard from the configured --listen value and the
// address the listener actually bound. The bound address supplies the port.
func NewGuardForAddr(listenAddr string, bound net.Addr, extraOrigins ...string) (*Guard, error) {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid listen address %q: %w", listenAddr, err)
	}
	_, port, err := net.SplitHostPort(bound.String())
	if err != nil {
		return nil, fmt.Errorf("invalid bound address %q: %w", bound.String(), err)
	}
	return NewGuard(host, port, extraOrigins...), nil
}

// HostAllowed reports whether a Host header names this server by a loopback
// name.
//
// The port is deliberately not compared. A rebinding page always carries its
// own hostname, never localhost, 127.0.0.1 or [::1], so matching the port adds
// nothing against rebinding. It would break `ssh -L 9000:127.0.0.1:4785` and
// the Vite dev proxy, whose clients send their own local port. Origin, which
// is what a browser uses to scope a request, is still matched exactly.
func (g *Guard) HostAllowed(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	_, ok := g.hosts[host]
	return ok
}

// OriginAllowed reports whether origin exactly matches (scheme, host and
// port) an allowed origin. An empty origin is not allowed here; callers
// decide what a request without an Origin header means.
func (g *Guard) OriginAllowed(origin string) bool {
	origin = strings.ToLower(strings.TrimSpace(origin))
	if origin == "" {
		return false
	}
	if u, err := url.Parse(origin); err == nil && u.Host != "" {
		origin = u.Scheme + "://" + u.Host
	}
	_, ok := g.origins[origin]
	return ok
}

// Origins returns the allowed Origin values, for handing to a transport that
// keeps its own list. It is never empty.
func (g *Guard) Origins() []string {
	out := make([]string, 0, len(g.origins))
	for origin := range g.origins {
		out = append(out, origin)
	}
	return out
}

// Messages written by Middleware. They are fixed strings with nothing
// credential-shaped in them, and a test holds them intact through redact.Text.
const (
	HostRejected   = "request rejected: Host header is not a loopback name for this server"
	OriginRejected = "request rejected: Origin is not allowed for this server"
)

// Middleware rejects, with 403, any request whose Host is not allowed or that
// carries an Origin outside the allowed set. It runs before any handler,
// static assets included.
func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.HostAllowed(r.Host) {
			reject(w, HostRejected)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !g.OriginAllowed(origin) {
			reject(w, OriginRejected)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func reject(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": msg})
}
