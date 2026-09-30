//go:build !darwin || !cgo

package main

// verify refuses where LocalAuthentication is not built in: another OS, or
// a build without cgo. The daemon refuses with the recovery named.
func verify(string) (int, string) {
	return 2, "cerberus-presence: user presence is checked only on macOS, in a build with cgo"
}
