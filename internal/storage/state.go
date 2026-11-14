package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/hollis-labs/libs/core/store"
)

// AllTargets is the pause_state target that pauses everything (old: the file "paused").
const AllTargets = "*"

// SetPaused records target as paused at now, or clears it. Pausing an already paused target
// keeps its first paused_at.
func SetPaused(ctx context.Context, st *store.Store, target string, paused bool, now time.Time) error {
	if target == "" {
		return errors.New("pause: empty target")
	}
	return st.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if !paused {
			_, err := tx.ExecContext(ctx, `DELETE FROM pause_state WHERE target = ?`, target)
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO pause_state (target, paused_at) VALUES (?, ?) ON CONFLICT (target) DO NOTHING`,
			target, now.UTC().Unix())
		return err
	})
}

// Paused reports whether target is paused, either itself or through AllTargets.
func Paused(ctx context.Context, st *store.Store, target string) (bool, error) {
	var n int
	err := st.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pause_state WHERE target IN (?, ?)`, target, AllTargets).Scan(&n)
	return n > 0, err
}

// PausedTargets lists every paused target, sorted.
func PausedTargets(ctx context.Context, st *store.Store) ([]string, error) {
	rows, err := st.Reader().QueryContext(ctx, `SELECT target FROM pause_state ORDER BY target`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetApplied records the digest resourceID was just applied with. An unchanged digest is
// left as it is.
func SetApplied(ctx context.Context, st *store.Store, resourceID, digest string, now time.Time) error {
	if resourceID == "" || digest == "" {
		return errors.New("applied definition: empty resource id or digest")
	}
	return st.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO applied_definitions (resource_id, digest, applied_at) VALUES (?, ?, ?)
ON CONFLICT (resource_id) DO UPDATE SET digest = excluded.digest, applied_at = excluded.applied_at
WHERE applied_definitions.digest <> excluded.digest`, resourceID, digest, now.UTC().Unix())
		return err
	})
}

// Applied is the digest resourceID was last applied with, if any.
func Applied(ctx context.Context, st *store.Store, resourceID string) (string, bool, error) {
	var d string
	err := st.Reader().QueryRowContext(ctx,
		`SELECT digest FROM applied_definitions WHERE resource_id = ?`, resourceID).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return d, err == nil, err
}

// Receipt is one credential-issuance lifecycle record. References name file:// paths, never
// values.
type Receipt struct {
	IssuanceID    string
	State         string
	Connector     string
	Operation     string
	BindingDigest string
	References    map[string]string
	At            time.Time
}

// ErrReceiptExists means the issuance already has a receipt for that state: a receipt is
// written once, as the old create-once files were.
var ErrReceiptExists = errors.New("credential issuance receipt already recorded for this state")

// RecordReceipt writes r once.
func RecordReceipt(ctx context.Context, st *store.Store, r Receipt) error {
	if r.IssuanceID == "" || r.State == "" {
		return errors.New("credential issuance receipt: empty id or state")
	}
	refs := r.References
	if refs == nil {
		refs = map[string]string{}
	}
	data, err := json.Marshal(refs)
	if err != nil {
		return fmt.Errorf("credential issuance receipt: %w", err)
	}
	err = st.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, execErr := tx.ExecContext(ctx, `INSERT INTO credential_issuance_receipts
(issuance_id, state, connector, operation, binding_digest, refs, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.IssuanceID, r.State, r.Connector, r.Operation, r.BindingDigest, string(data),
			r.At.UTC().Format(time.RFC3339Nano))
		return execErr
	})
	if store.IsUniqueViolation(err) {
		return ErrReceiptExists
	}
	return err
}

// stateOrder is the lifecycle order receipts are returned in.
var stateOrder = map[string]int{"preparing": 0, "prepared": 1, "dispatching": 2, "completed": 3, "uncertain": 3}

// Receipts returns an issuance's receipts in lifecycle order.
func Receipts(ctx context.Context, st *store.Store, issuanceID string) ([]Receipt, error) {
	rows, err := st.Reader().QueryContext(ctx, `SELECT issuance_id, state, connector, operation, binding_digest, refs, at
FROM credential_issuance_receipts WHERE issuance_id = ?`, issuanceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Receipt
	for rows.Next() {
		var r Receipt
		var refs, at string
		if scanErr := rows.Scan(&r.IssuanceID, &r.State, &r.Connector, &r.Operation, &r.BindingDigest, &refs, &at); scanErr != nil {
			return nil, scanErr
		}
		if jsonErr := json.Unmarshal([]byte(refs), &r.References); jsonErr != nil {
			return nil, fmt.Errorf("credential issuance %s %s: refs: %w", r.IssuanceID, r.State, jsonErr)
		}
		var parseErr error
		if r.At, parseErr = time.Parse(time.RFC3339Nano, at); parseErr != nil {
			return nil, fmt.Errorf("credential issuance %s %s: at: %w", r.IssuanceID, r.State, parseErr)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return stateOrder[out[i].State] < stateOrder[out[j].State] })
	return out, nil
}
