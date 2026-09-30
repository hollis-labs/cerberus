package secrets

import (
	"os"
	"testing"
)

// TestMain replaces the OS credential store with an in-memory mock: these
// tests exercise KeychainProvider, and must never read or write the
// operator's keychain.
func TestMain(m *testing.M) {
	MockStoreForTests()
	os.Exit(m.Run())
}
