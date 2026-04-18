//go:build darwin

package procscan

import "syscall"

// statDev normalizes Stat_t.Dev to a uint64. On darwin Dev is int32; on
// linux it's already uint64. This wrapper avoids platform-dependent
// "unnecessary conversion" lint warnings.
func statDev(st *syscall.Stat_t) uint64 {
	return uint64(st.Dev) //nolint:gosec // Dev is a signed device number; uint64 reinterpret is intentional
}
