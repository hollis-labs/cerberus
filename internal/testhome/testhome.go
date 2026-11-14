// Package testhome keeps a package's tests out of the operator's home.
//
// Cerberus resolves its state from the home directory at run time
// (~/.cerberus: pid and lock files, pause markers, plugin data, the audit log)
// and from the XDG directories (the workspace database). A test that reaches
// any of it without a home of its own writes into the live tree of whoever runs
// the suite. On 2026-11-14 a pre-push `go test ./...` did exactly that: it
// removed a live dev session's pid file and created plugin directories under
// the operator's ~/.cerberus (CW-20261114-0143).
//
// Every package with tests runs them through Main (or calls Isolate first in a
// TestMain of its own), and internal/hygiene fails the suite for a test package
// that does not.
package testhome

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Vars are the environment variables Isolate points at the scratch home. The
// last one disconnects the session bus, so no test reaches the operator's OS
// credential store (Secret Service) by accident; a test that needs one mocks it.
var Vars = []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "DBUS_SESSION_BUS_ADDRESS"}

// Isolate points HOME and the XDG directories at a fresh scratch home, and
// returns the function that removes it. The scratch home is under /tmp when
// there is one, because some tests listen on a unix socket under
// ~/.cerberus and a socket path is limited to 108 bytes.
func Isolate() (cleanup func(), err error) {
	base := ""
	if fi, statErr := os.Stat("/tmp"); statErr == nil && fi.IsDir() {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "cerb-test-home-")
	if err != nil {
		return nil, fmt.Errorf("testhome: %w", err)
	}
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("testhome: %w", err)
	}
	values := map[string]string{
		"HOME":                     home,
		"XDG_CONFIG_HOME":          filepath.Join(home, ".config"),
		"XDG_DATA_HOME":            filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME":           filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME":           filepath.Join(home, ".cache"),
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + filepath.Join(dir, "no-session-bus"),
	}
	for _, k := range Vars {
		if err := os.Setenv(k, values[k]); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("testhome: set %s: %w", k, err)
		}
	}
	return func() { _ = os.RemoveAll(dir) }, nil
}

// Main runs a package's tests under Isolate and exits with their result. Use
// it as the package's TestMain:
//
//	func TestMain(m *testing.M) { testhome.Main(m) }
func Main(m *testing.M) {
	cleanup, err := Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
