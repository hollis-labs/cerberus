package legacyimport

import (
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/legacyimport/oldtree"
)

// readAnchor is the raw keychain item under account (oldtree.ReadAnchor).
func readAnchor(account string) (raw string, head audit.Head, ok bool, err error) {
	return oldtree.ReadAnchor(account)
}

// memoryAnchor holds a head for a check, and refuses to move it.
type memoryAnchor = oldtree.MemoryAnchor

// checkChain is oldtree.CheckChain.
func checkChain(dir string, anchor audit.Anchor) (audit.Checked, string) {
	return oldtree.CheckChain(dir, anchor)
}
