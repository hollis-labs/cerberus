//go:build !linux

package userpresence

import "context"

// macOS raises its own LocalAuthentication prompt from the daemon.
func StartTerminalAgent(context.Context) (func(), error) { return func() {}, nil }
