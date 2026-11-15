//go:build !unix

package main

import "os"

// fileOwner is not known off unix; the mode check still applies.
func fileOwner(os.FileInfo) (int, bool) { return 0, false }
