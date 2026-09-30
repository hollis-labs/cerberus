package cerbapi

import (
	"fmt"
	"os"
	"testing"
)

// TestMain holds every test's transfers to a scratch root: a test that runs
// an ssh transfer without a CLI principal is confined (B3), and the default
// root is under the operator's real home.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "cerb-transfers-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	SetTransferRoot(root)
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
