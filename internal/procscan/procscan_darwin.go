//go:build darwin

package procscan

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func init() {
	defaultEnumerator = darwinEnumerator{}
}

// darwinEnumerator implements PIDEnumerator on macOS. PIDs() uses
// proc_listpids via SYS_PROC_INFO; Fingerprint() resolves the executable
// path via PROC_PIDPATHINFO and stats it for (device, inode).
type darwinEnumerator struct{}

// Darwin proc_info call numbers + flavors (xnu/bsd/sys/proc_info.h).
//
// The kernel's __proc_info syscall takes:
//
//	__proc_info(callnum, pid, flavor, arg, buffer, buffersize)
//
// For LISTPIDS the second slot doubles as the "type" selector.
const (
	procInfoCallListPIDs  = 1
	procInfoCallPIDInfo   = 2
	procAllPIDs           = 1
	procPIDPathInfo       = 11
	procPidPathInfoMaxLen = 4 * 1024 // PROC_PIDPATHINFO_MAXSIZE = 4*MAXPATHLEN
)

// PIDs returns all PIDs visible to the kernel (the kernel filters out
// processes the caller can't see). Subsequent uid filtering happens
// implicitly: PROC_PIDPATHINFO returns EPERM for processes owned by
// other users, which Fingerprint() treats as "skip".
//
// We invoke __proc_info via syscall.Syscall6 directly because the
// libSystem wrappers (proc_listpids, proc_pidpath) require cgo, and
// the Cerberus build is intentionally cgo-free. SYS_PROC_INFO is
// stable across darwin releases — Apple's deprecation note refers to
// the C API, not the syscall number itself.
//
//nolint:staticcheck // SA1019: SYS_PROC_INFO is the cgo-free path
func (darwinEnumerator) PIDs() ([]int, error) {
	// Two-step pattern: call with buf=nil to learn required size, then
	// allocate and call again. proc_listpids returns the byte length of
	// the PID array, not a count.
	required, _, e := syscall.Syscall6(
		unix.SYS_PROC_INFO,
		uintptr(procInfoCallListPIDs),
		uintptr(procAllPIDs),
		0,
		0,
		0,
		0,
	)
	if e != 0 {
		return nil, fmt.Errorf("proc_listpids size probe: %w", error(e))
	}
	if required == 0 {
		return nil, nil
	}
	// Pad in case the process table grew between calls. Bound the
	// allocation: a sane upper limit is ~1M PIDs (4 MiB), well above
	// any plausible system. uintptr → int conversion is safe in this
	// regime; the gosec G115 guard is defensive.
	if required > 1<<24 { //nolint:gosec // bounded upper limit
		return nil, fmt.Errorf("proc_listpids: implausible required=%d", required)
	}
	bufLen := int(required) + 64*4 //nolint:gosec // bounded above
	buf := make([]int32, bufLen/4)

	written, _, e := syscall.Syscall6(
		unix.SYS_PROC_INFO,
		uintptr(procInfoCallListPIDs),
		uintptr(procAllPIDs),
		0,
		0,
		uintptr(unsafe.Pointer(&buf[0])), //nolint:gosec // G103: buf is heap-allocated, lifetime tied to call
		uintptr(bufLen),
	)
	if e != 0 {
		return nil, fmt.Errorf("proc_listpids: %w", error(e))
	}
	count := int(written) / 4 //nolint:gosec // written is bounded by bufLen above
	if count > len(buf) {
		count = len(buf)
	}
	pids := make([]int, 0, count)
	for i := 0; i < count; i++ {
		if buf[i] > 0 {
			pids = append(pids, int(buf[i]))
		}
	}
	return pids, nil
}

// Fingerprint resolves pid → executable path via proc_pidpath, then
// stats the path for (device, inode). Returns an error if the kernel
// declines the request (process gone, permission denied, etc).
//
//nolint:staticcheck // SA1019: SYS_PROC_INFO is the cgo-free path
func (darwinEnumerator) Fingerprint(pid int) (BinaryFingerprint, error) {
	if pid <= 0 {
		return BinaryFingerprint{}, fmt.Errorf("invalid pid %d", pid)
	}
	buf := make([]byte, procPidPathInfoMaxLen)
	// __proc_info returns 0 on success and writes a NUL-terminated path
	// into buf. (Apple's libproc proc_pidpath wrapper then computes
	// strlen(buf) to derive the byte count.) We must trust errno for
	// success/failure and parse the buffer for the path — NOT the
	// return value.
	_, _, e := syscall.Syscall6(
		unix.SYS_PROC_INFO,
		uintptr(procInfoCallPIDInfo),
		uintptr(pid),
		uintptr(procPIDPathInfo),
		0,
		uintptr(unsafe.Pointer(&buf[0])), //nolint:gosec // G103: buf is heap-allocated, lifetime tied to call
		uintptr(len(buf)),
	)
	if e != 0 {
		return BinaryFingerprint{}, fmt.Errorf("proc_pidpath %d: %w", pid, error(e))
	}
	end := len(buf)
	for i := 0; i < len(buf); i++ {
		if buf[i] == 0 {
			end = i
			break
		}
	}
	path := string(buf[:end])
	if path == "" {
		return BinaryFingerprint{}, fmt.Errorf("proc_pidpath %d: empty path", pid)
	}

	info, err := os.Stat(path)
	if err != nil {
		return BinaryFingerprint{}, fmt.Errorf("stat %s: %w", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return BinaryFingerprint{}, fmt.Errorf("stat %s: no Stat_t", path)
	}
	return BinaryFingerprint{
		Device: statDev(st),
		Inode:  st.Ino,
		Path:   path,
	}, nil
}
