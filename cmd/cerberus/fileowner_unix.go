//go:build unix

package main

import (
	"os"
	"syscall"
)

// fileOwner is the uid that owns fi.
func fileOwner(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
