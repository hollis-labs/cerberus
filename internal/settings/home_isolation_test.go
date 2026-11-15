package settings

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

// TestMain keeps these tests out of the operator's home (internal/testhome).
func TestMain(m *testing.M) {
	// A scratch HOME does not protect a macOS Keychain: the OS credential store is mocked too.
	secrets.MockStoreForTests()
	testhome.Main(m)
}
