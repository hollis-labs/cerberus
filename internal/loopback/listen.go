package loopback

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
)

// SquattedError is the other loopback family's address, on the same port,
// already held by another process.
type SquattedError struct {
	Addr string
	Port string
}

func (e *SquattedError) Error() string {
	return fmt.Sprintf("%s is already bound by another process, and a browser may try it first for localhost: it would receive your sign-in and approval links and your session. "+
		"Find it with `lsof -nP -iTCP:%s -sTCP:LISTEN` and stop it, or choose another port with --listen", e.Addr, e.Port)
}

// ListenBoth binds a loopback listen address on both loopback families, on
// one port. A browser resolves localhost to ::1 as well as 127.0.0.1 and may
// try ::1 first, so a server that holds only 127.0.0.1 leaves [::1] on its
// port to any other local account, which then receives every link the
// server hands out as http://localhost (H6).
//
// If the other family's address on that port is taken, it refuses, with a
// *SquattedError, rather than start beside a squatter. A machine with no
// IPv6 loopback serves 127.0.0.1 alone: no browser there can reach ::1
// either. A non-loopback address is bound as given.
func ListenBoth(addr string) ([]net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	var first, second string
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "127.0.0.1", "localhost":
		first, second = "127.0.0.1", "::1"
	case "::1":
		first, second = "::1", "127.0.0.1"
	default:
		ln, lerr := net.Listen("tcp", addr)
		if lerr != nil {
			return nil, lerr
		}
		return []net.Listener{ln}, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(first, port))
	if err != nil {
		return nil, err
	}
	_, bound, _ := net.SplitHostPort(ln.Addr().String())
	other, err := net.Listen("tcp", net.JoinHostPort(second, bound))
	if err != nil {
		switch {
		case errors.Is(err, syscall.EADDRINUSE):
			_ = ln.Close()
			return nil, &SquattedError{Addr: net.JoinHostPort(second, bound), Port: bound}
		case second == "::1" && noIPv6(err):
			return []net.Listener{ln}, nil
		default:
			_ = ln.Close()
			return nil, fmt.Errorf("listen %s: %w", net.JoinHostPort(second, bound), err)
		}
	}
	return []net.Listener{ln, other}, nil
}

// noIPv6 reports an error that means this machine has no IPv6 loopback.
func noIPv6(err error) bool {
	return errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EPROTONOSUPPORT)
}
