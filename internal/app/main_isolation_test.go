package app

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/testhome"
)

// TestMain points HOME at a scratch directory for the whole package, before
// any test runs. The audit sink is opened once per process under
// $HOME/.cerberus/audit, so without this a test that reaches it would write
// records into the operator's real audit log.
func TestMain(m *testing.M) {
	testhome.Main(m)
}
