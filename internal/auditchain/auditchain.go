// Package auditchain opens the Substrate build's audit chain: the old build's chain, copied into
// the data directory and continued in the same format (docs/substrate-port-plan.md, sections 4.2
// and 5), with its head anchored in the login keychain under the new directory's path.
package auditchain

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

// AnchorService is the keychain service the chain's head is kept under, one item per audit
// directory (the account is the directory's path). It is the old build's name: a published
// contract, so the old binary verifies a chain the new one continued.
const AnchorService = "cerberus-audit-anchor"

// KeychainAnchor keeps an audit directory's chain head in the login keychain (the old build's
// keychainAnchor).
type KeychainAnchor struct{ Dir string }

// Load implements audit.Anchor.
func (k KeychainAnchor) Load() (audit.Head, bool, error) {
	data, err := secrets.KeyringGet(AnchorService, k.Dir)
	if errors.Is(err, keyring.ErrNotFound) {
		return audit.Head{}, false, nil
	}
	if err != nil {
		return audit.Head{}, false, fmt.Errorf("keychain: %w", err)
	}
	var h audit.Head
	if err := json.Unmarshal([]byte(data), &h); err != nil {
		return audit.Head{}, false, fmt.Errorf("keychain item %s does not parse", AnchorService)
	}
	return h, true, nil
}

// Store implements audit.Anchor.
func (k KeychainAnchor) Store(h audit.Head) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return secrets.KeyringSet(AnchorService, k.Dir, string(data))
}

// Open installs the keychain anchor for dir and opens the chain there, continuing it from its
// head. The digest key is the directory's .digest_key, the one applied digests are keyed with.
func Open(dir string) (*audit.FileSink, error) {
	audit.SetAnchor(dir, KeychainAnchor{Dir: dir})
	return audit.OpenFileSink(dir)
}
