package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/hollis-labs/libs/core/store"
	"gopkg.in/yaml.v3"

	"github.com/hollis-labs/cerberus/internal/credentialbroker"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/storage"
)

// ConnectorConfig reads cerberus.connectors, the old connector-config.yaml as config keys: the
// same shape (plugin id to fields, mcp.expose and limits), decoded with unknown keys refused.
// key names the block in errors. Its fingerprint is the sha256 of the block re-encoded as YAML.
func ConnectorConfig(raw any, key string) (pluginhost.ConnectorConfig, error) {
	cfg := pluginhost.ConnectorConfig{Path: key, Entries: map[string]pluginhost.PluginSettings{}}
	if raw == nil {
		return cfg, nil
	}
	if _, ok := raw.(map[string]any); !ok {
		return cfg, fmt.Errorf("%s must be a map of plugin id to its settings", key)
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		return cfg, fmt.Errorf("%s: %w", key, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg.Entries); err != nil && !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("%s: %w", key, err)
	}
	if cfg.Entries == nil {
		cfg.Entries = map[string]pluginhost.PluginSettings{}
	}
	sum := sha256.Sum256(data)
	cfg.SHA256 = hex.EncodeToString(sum[:])
	return cfg, nil
}

// StoreRecorder keeps the credential broker's receipts in the class A store
// (credential_issuance_receipts). Inserting the preparing receipt is the exclusive
// reservation: a second one for the same id is ErrReceiptExists. A committed write is durable,
// so the broker's order holds: each issued secret is written and fsynced before the prepared
// receipt is recorded.
type StoreRecorder struct{ Store *store.Store }

var _ credentialbroker.Recorder = StoreRecorder{}

// Reserve records the preparing receipt, or refuses an id that has one.
func (r StoreRecorder) Reserve(ctx context.Context, rc credentialbroker.Receipt) error {
	err := storage.RecordReceipt(ctx, r.Store, receiptRow(rc))
	if errors.Is(err, storage.ErrReceiptExists) {
		return errors.New("credential issuance ID is already reserved; inspect its durable record before retrying")
	}
	if err != nil {
		return errors.New("credential issuance reservation could not be recorded")
	}
	return nil
}

// Record records a later receipt, once.
func (r StoreRecorder) Record(ctx context.Context, rc credentialbroker.Receipt) error {
	if err := storage.RecordReceipt(ctx, r.Store, receiptRow(rc)); err != nil {
		return errors.New("credential issuance record could not be persisted")
	}
	return nil
}

func receiptRow(rc credentialbroker.Receipt) storage.Receipt {
	return storage.Receipt{IssuanceID: rc.ID, State: rc.State, Connector: rc.Connector, Operation: rc.Operation,
		BindingDigest: rc.Digest, References: rc.References, At: rc.At}
}
