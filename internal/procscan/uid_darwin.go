//go:build darwin

package procscan

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// kInfoProc is the layout returned by sysctl({CTL_KERN, KERN_PROC,
// KERN_PROC_PID, pid}). We only care about the effective uid stored in
// kp_eproc.e_pcred.p_ruid (offset varies by platform).
//
// The simplest portable approach on darwin is to use the dedicated
// PROC_PIDTBSDINFO flavor of __proc_info, which returns a fixed-size
// proc_bsdinfo struct that includes pbi_uid. That keeps us out of the
// fragile kinfo_proc struct-layout business.
//
// proc_bsdinfo layout (xnu/bsd/sys/proc_info.h):
//
//	uint32_t pbi_flags;             0
//	uint32_t pbi_status;            4
//	uint32_t pbi_xstatus;           8
//	uint32_t pbi_pid;              12
//	uint32_t pbi_ppid;             16
//	uid_t    pbi_uid;              20  <-- what we want
//	gid_t    pbi_gid;              24
//	uid_t    pbi_ruid;             28
//	gid_t    pbi_rgid;             32
//	uid_t    pbi_svuid;            36
//	gid_t    pbi_svgid;            40
//	... (more fields)
//
// Total size is 168 bytes (PROC_PIDTBSDINFO_SIZE).

const (
	procPidTBSDInfo     = 3
	procPidTBSDInfoSize = 168
	procBSDInfoUIDOff   = 20
)

// pidUID returns the effective uid of pid by querying
// proc_pidinfo(pid, PROC_PIDTBSDINFO, ...). Returns an error when the
// process is gone or inaccessible.
//
//nolint:staticcheck // SA1019: SYS_PROC_INFO is the cgo-free path
func pidUID(pid int) (int, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d", pid)
	}
	buf := make([]byte, procPidTBSDInfoSize)
	_, _, e := syscall.Syscall6(
		unix.SYS_PROC_INFO,
		uintptr(procInfoCallPIDInfo),
		uintptr(pid),
		uintptr(procPidTBSDInfo),
		0,
		uintptr(unsafe.Pointer(&buf[0])), //nolint:gosec // G103: buf is heap-allocated, lifetime tied to call
		uintptr(len(buf)),
	)
	if e != 0 {
		return 0, fmt.Errorf("proc_pidinfo(TBSDINFO) %d: %w", pid, error(e))
	}
	uid := *(*uint32)(unsafe.Pointer(&buf[procBSDInfoUIDOff])) //nolint:gosec // G103: fixed-offset read into known-size struct
	return int(uid), nil
}
