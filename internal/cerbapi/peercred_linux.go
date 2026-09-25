//go:build linux

package cerbapi

import "golang.org/x/sys/unix"

// readPeerCred uses SO_PEERCRED.
func readPeerCred(fd int) peerCred {
	uc, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return peerCred{uid: -1, err: err}
	}
	return peerCred{uid: int(uc.Uid), pid: int(uc.Pid)}
}
