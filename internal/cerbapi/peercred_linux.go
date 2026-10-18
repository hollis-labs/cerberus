//go:build linux

package cerbapi

import (
	"github.com/hollis-labs/cerberus/internal/userpresence"
	"golang.org/x/sys/unix"
)

// readPeerCred uses SO_PEERCRED.
func readPeerCred(fd int) peerCred {
	uc, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return peerCred{uid: -1, err: err}
	}
	start, _ := userpresence.ProcessStartTime(int(uc.Pid))
	return peerCred{uid: int(uc.Uid), pid: int(uc.Pid), startTime: start}
}
