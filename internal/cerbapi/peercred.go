package cerbapi

import (
	"context"
	"errors"
	"net"
)

// peerCred is what the kernel says about the other end of a socket
// connection.
type peerCred struct {
	uid int
	pid int
	err error
}

type peerKey struct{}

func withPeer(ctx context.Context, p peerCred) context.Context {
	return context.WithValue(ctx, peerKey{}, p)
}

func peerFrom(ctx context.Context) (peerCred, bool) {
	p, ok := ctx.Value(peerKey{}).(peerCred)
	return p, ok
}

// kernelPeerCred reads the peer credentials of a unix socket connection.
func kernelPeerCred(c net.Conn) peerCred {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return peerCred{uid: -1, err: errors.New("not a unix socket connection")}
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return peerCred{uid: -1, err: err}
	}
	var cred peerCred
	ctrlErr := raw.Control(func(fd uintptr) { cred = readPeerCred(int(fd)) }) //nolint:gosec // fd fits in int
	if ctrlErr != nil {
		return peerCred{uid: -1, err: ctrlErr}
	}
	return cred
}
