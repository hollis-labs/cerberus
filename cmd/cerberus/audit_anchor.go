package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"

	"github.com/hollis-labs/cerberus/internal/audit"
)

// auditAnchorService is the keychain service the audit chain's head is
// kept under, one item per audit directory (M2).
const auditAnchorService = "cerberus-audit-anchor"

// keychainAnchor keeps an audit directory's chain head in the login
// keychain: outside ~/.cerberus, though not outside the operator's account
// (audit.Anchor says what that does and does not catch).
type keychainAnchor struct{ dir string }

func (k keychainAnchor) Load() (audit.Head, bool, error) {
	data, err := keyring.Get(auditAnchorService, k.dir)
	if errors.Is(err, keyring.ErrNotFound) {
		return audit.Head{}, false, nil
	}
	if err != nil {
		return audit.Head{}, false, fmt.Errorf("keychain: %w", err)
	}
	var h audit.Head
	if err := json.Unmarshal([]byte(data), &h); err != nil {
		return audit.Head{}, false, fmt.Errorf("keychain item %s does not parse", auditAnchorService)
	}
	return h, true, nil
}

func (k keychainAnchor) Store(h audit.Head) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return keyring.Set(auditAnchorService, k.dir, string(data))
}

// installAuditAnchor installs the keychain anchor for this process's audit
// directory. The daemon installs it, since it writes nearly every record;
// audit verify and audit reanchor install it, to check against it and to
// move it. Tests swap it.
var installAuditAnchor = func() {
	if dir, err := auditDir(); err == nil {
		audit.SetAnchor(dir, keychainAnchor{dir: dir})
	}
}
