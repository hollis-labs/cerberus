package legacyimport

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

// readAnchor is the raw keychain item under account (the audit directory's path), as the old
// build stored it: JSON {seq, hash, file, offset}. ok is false when there is none.
func readAnchor(account string) (raw string, head audit.Head, ok bool, err error) {
	raw, err = secrets.KeyringGet(auditchain.AnchorService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", audit.Head{}, false, nil
	}
	if err != nil {
		return "", audit.Head{}, false, fmt.Errorf("read the %s keychain item for %s: %w", auditchain.AnchorService, account, err)
	}
	if err = json.Unmarshal([]byte(raw), &head); err != nil || head.Seq == 0 || head.Hash == "" {
		return raw, head, true, fmt.Errorf("the %s keychain item for %s does not parse as a chain head", auditchain.AnchorService, account)
	}
	return raw, head, true, nil
}

// memoryAnchor holds a head for a check, and refuses to move it.
type memoryAnchor struct{ head audit.Head }

func (m memoryAnchor) Load() (audit.Head, bool, error) { return m.head, true, nil }
func (memoryAnchor) Store(audit.Head) error {
	return errors.New("migrate-from checks the chain and never writes to it")
}

// checkChain runs audit.Check on dir with anchor installed for it, and says whether it verifies:
// no problem, and the chain vouches for its newest record. The anchor is removed afterwards
// (audit.SetAnchor holds one directory per process, so each check installs its own).
func checkChain(dir string, anchor audit.Anchor) (audit.Checked, string) {
	audit.SetAnchor(dir, anchor)
	defer audit.SetAnchor("", nil)
	checked, err := audit.Check(dir)
	switch {
	case err != nil:
		return checked, "error: " + err.Error()
	case len(checked.Problems) > 0:
		return checked, "problems: " + checked.Err().Error()
	case !checked.TailTrusted():
		return checked, "the chain does not vouch for its newest record"
	}
	return checked, "ok"
}
