package secretref_test

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

// TestMain keeps these tests out of the operator's home (internal/testhome) and off the OS
// credential store, which a scratch HOME does not protect on macOS: secretref links the keyring
// library through libs/security/secrets. It is in the external test package because
// internal/secrets imports secretref.
func TestMain(m *testing.M) {
	secrets.MockStoreForTests()
	testhome.Main(m)
}
