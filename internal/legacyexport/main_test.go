package legacyexport

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

func TestMain(m *testing.M) {
	// The anchors live in the OS credential store: mocked, never the operator's.
	secrets.MockStoreForTests()
	testhome.Main(m)
}
