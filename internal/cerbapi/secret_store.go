package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/hollis-labs/cerberus/internal/audit"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

// secretNamePart is one half of a stored secret's name: a connector or
// plugin id, or a secret name.
var secretNamePart = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

func secretStoreOperation() contract.Operation {
	return contract.Operation{
		Name: "set", Effect: contract.EffectAdmin,
		Target:  contract.TargetDescriptor{Kind: "secret", From: []string{"name"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone,
	}.Finalize()
}

// SetStoredSecret writes one entry to the OS credential store, the store
// every keyring:// reference and every connector's last fallback reads. It is
// how an operator stores a secret backend's own credential (a Keeper
// configuration, a 1Password service account token), which must not come
// from another vault.
//
// The write is recorded, by name: the value never reaches the record. Its
// intent is written first, and an unwritable log refuses the write.
func SetStoredSecret(ctx context.Context, sink audit.Sink, store secret.ReadWriter, service, key, value string) error {
	if !secretNamePart.MatchString(service) || !secretNamePart.MatchString(key) {
		return fmt.Errorf("%q is not a secret name: use <connector or plugin id>/<secret name>, lowercase, for example keeper/ksm_config", service+"/"+key)
	}
	if value == "" {
		return errors.New("the value is empty; nothing was stored")
	}
	spec := auditSpec{connector: "secrets", operation: "set", op: secretStoreOperation(), known: true,
		config: map[string]any{"name": service + "/" + key}}
	call, err := beginAudit(ctx, sink, slog.Default(), spec)
	if err != nil {
		return fmt.Errorf("the audit log could not be written, so nothing was stored: %w", err)
	}
	err = store.Set(ctx, service, key, value)
	call.finish(err)
	return err
}
