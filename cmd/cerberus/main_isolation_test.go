package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

// TestMain points HOME at a scratch directory for the whole package, before
// any test runs. The audit sink is opened once per process under
// $HOME/.cerberus/audit, so without this a test that reaches it would write
// records into the operator's real audit log.
func TestMain(m *testing.M) {
	// The daemon blocked-credential-store test re-executes this test binary as
	// a daemon whose credential store never answers.
	if os.Getenv(childDaemonEnv) == "1" {
		runChildDaemon()
		return
	}
	cleanupHome, err := testhome.Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// The --config default was resolved at package init, from the home the
	// test binary started with; resolve it again under the scratch home, or a
	// command without --config reaches the operator's real ~/.cerberus
	// (plugin-data and plugin-cache, CW-20261114-0143).
	cfgPath = config.DefaultPath()
	// Tests never read or write the operator's real credential store: the
	// CLI reaches KeychainProvider through the connector chain and the
	// console's store.
	secrets.MockStoreForTests()
	// The tests run under the scratch HOME above, which is exactly what
	// the in-process state check refuses in real use (M11).
	inProcessStateCheck = func() error { return nil }
	code := m.Run()
	cleanupHome()
	os.Exit(code)
}
