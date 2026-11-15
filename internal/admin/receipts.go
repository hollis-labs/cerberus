package admin

import (
	"context"
	"errors"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/credentialbroker"
	"github.com/hollis-labs/cerberus/internal/storage"
)

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
