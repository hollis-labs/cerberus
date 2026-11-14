package secrets

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/testhome"
)

// TestMain replaces the OS credential store with an in-memory mock: these
// tests exercise KeychainProvider, and must never read or write the
// operator's keychain.
func TestMain(m *testing.M) {
	MockStoreForTests()
	testhome.Main(m) // and under a scratch home (internal/testhome)
}
