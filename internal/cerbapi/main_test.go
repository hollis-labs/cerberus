package cerbapi

import (
	"fmt"
	"os"
	"testing"

	"github.com/hollis-labs/cerberus/internal/testhome"
)

// TestMain holds every test's transfers to a scratch root: a test that runs
// an ssh transfer without a CLI principal is confined (B3), and the default
// root is under the operator's real home.
//
// Every test also runs under a scratch home (internal/testhome): the runtime
// writes pid, lock and pause files under ~/.cerberus.
func TestMain(m *testing.M) {
	cleanupHome, homeErr := testhome.Isolate()
	if homeErr != nil {
		fmt.Fprintln(os.Stderr, homeErr)
		os.Exit(1)
	}
	root, err := os.MkdirTemp("", "cerb-transfers-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	SetTransferRoot(root)
	code := m.Run()
	_ = os.RemoveAll(root)
	cleanupHome()
	os.Exit(code)
}
