package userpresence

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/testhome"
)

// TestMain keeps these tests out of the operator's home (internal/testhome).
func TestMain(m *testing.M) { testhome.Main(m) }
