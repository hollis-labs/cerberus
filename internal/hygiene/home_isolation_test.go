package hygiene

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/testhome"
)

// TestMain keeps these tests out of the operator's home (internal/testhome), after reading the
// toolchain's caches for the go list the keyring check runs.
func TestMain(m *testing.M) {
	goToolEnv = readGoToolEnv()
	testhome.Main(m)
}
