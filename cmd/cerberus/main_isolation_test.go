package main

import (
	"fmt"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"os"
	"testing"
)

// TestMain points HOME at a scratch directory for the whole package, before
// any test runs. The audit sink is opened once per process under
// $HOME/.cerberus/audit, so without this a test that reaches it would write
// records into the operator's real audit log.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("/tmp", "cerb-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", home); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Tests never read or write the operator's real credential store: the
	// CLI reaches KeychainProvider through the connector chain and the
	// console's store.
	secrets.MockStoreForTests()
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
