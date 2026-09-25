//go:build darwin

package cerbapi

import "golang.org/x/sys/unix"

// readPeerCred uses LOCAL_PEERCRED for the uid (getpeereid's source) and
// LOCAL_PEERPID for the pid.
func readPeerCred(fd int) peerCred {
	xu, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return peerCred{uid: -1, err: err}
	}
	pid, _ := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	return peerCred{uid: int(xu.Uid), pid: pid}
}
