// Package credentialbroker persists host-generated credentials before handing
// them to one approved plugin operation. It never returns a value in a DTO.
package credentialbroker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	shared "github.com/hollis-labs/libs/security/secrets"
)

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var connectorPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// Binding is metadata only. Digest is the host's keyed digest of the complete
// operation arguments plus installed plugin/config identity, never a password.
type Binding struct {
	ID        string `json:"id"`
	Connector string `json:"connector"`
	Operation string `json:"operation"`
	Digest    string `json:"binding_digest"`
}
type Receipt struct {
	Binding
	State      string            `json:"state"`
	References map[string]string `json:"references"`
	At         time.Time         `json:"at"`
}

// Broker is safe to share: the recorder's exclusive reservation arbitrates
// concurrent requests (by default, exclusive directory creation, across
// processes). A reservation is never deleted or automatically replayed,
// including when preparation or a later operation fails.
type Broker struct {
	root   string
	store  *shared.Resolver
	random io.Reader
	rec    Recorder
}

// Recorder keeps an issuance's lifecycle receipts. Each receipt is written
// once and durably before Prepare, Start or Finish returns, so a receipt is
// never behind what was done. Receipts carry names and file:// references,
// never a value.
type Recorder interface {
	// Reserve claims r.ID exclusively and records its "preparing" receipt.
	// It fails for an id that was ever reserved, by any broker sharing the
	// recorder: that is what keeps an issuance from being replayed.
	Reserve(ctx context.Context, r Receipt) error
	// Record writes the receipt for r.State, once.
	Record(ctx context.Context, r Receipt) error
}

// Option configures a Broker.
type Option func(*Broker)

// WithRecorder keeps receipts in rec instead of the files under the
// broker's root. The issued secrets stay files under <root>/secrets/issued.
func WithRecorder(rec Recorder) Option {
	return func(b *Broker) {
		if rec != nil {
			b.rec = rec
		}
	}
}

// New is a broker over root: issued secrets under <root>/secrets/issued, and,
// unless WithRecorder says otherwise, receipts as create-once files under
// <root>/<id>/<state>.json.
func New(root string, opts ...Option) *Broker {
	b := &Broker{root: root, store: shared.New(shared.WithStateDir(filepath.Join(root, "secrets"))), random: rand.Reader, rec: FileRecorder{Root: root}}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// FileRecorder is the default recorder: an issuance's receipts are files
// <Root>/<id>/<state>.json, each created once and fsynced with its
// directory. Creating <Root>/<id> is the exclusive reservation.
type FileRecorder struct{ Root string }

// Reserve creates the issuance's directory, exclusively, and its preparing
// receipt.
func (f FileRecorder) Reserve(_ context.Context, r Receipt) error {
	dir := filepath.Join(f.Root, r.ID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return errors.New("credential issuance ID is already reserved or unavailable; inspect its durable record before retrying")
	}
	if err := syncDir(f.Root); err != nil {
		return errors.New("credential issuance reservation could not be synced")
	}
	return writeReceipt(dir, r)
}

// Record creates the receipt for r.State in the issuance's directory.
func (f FileRecorder) Record(_ context.Context, r Receipt) error {
	return writeReceipt(filepath.Join(f.Root, r.ID), r)
}

func writeReceipt(dir string, r Receipt) error {
	data, err := json.Marshal(r)
	if err != nil {
		return errors.New("credential issuance record encoding failed")
	}
	f, err := os.OpenFile(filepath.Join(dir, r.State+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // dir is a reserved host path; state is an internal lifecycle constant
	if err != nil {
		return errors.New("credential issuance record could not be created")
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("credential issuance record could not be persisted")
	}
	if syncDir(dir) != nil {
		return errors.New("credential issuance record directory could not be synced")
	}
	return nil
}

// Dispatch deliberately cannot be serialized or constructed by a wire caller.
// The returned values belong only in private Init config and redaction scopes.
type Dispatch struct {
	receipt Receipt
	values  map[string]string
	dir     string
	rec     Recorder
}

func (d *Dispatch) Receipt() Receipt {
	r := d.receipt
	r.References = clone(d.receipt.References)
	return r
}
func (d *Dispatch) Values() map[string]string { return clone(d.values) }
func clone(m map[string]string) map[string]string {
	n := map[string]string{}
	for k, v := range m {
		n[k] = v
	}
	return n
}
func (d *Dispatch) String() string               { data, _ := json.Marshal(d.Receipt()); return string(data) }
func (d *Dispatch) GoString() string             { return d.String() }
func (d *Dispatch) MarshalJSON() ([]byte, error) { return json.Marshal(d.Receipt()) }

func (b *Broker) Prepare(ctx context.Context, bind Binding, names []string) (*Dispatch, error) {
	if b == nil || !filepath.IsAbs(b.root) || !idPattern.MatchString(bind.ID) || !connectorPattern.MatchString(bind.Connector) || !namePattern.MatchString(bind.Operation) || bind.Digest == "" || len(names) == 0 || len(names) > 8 {
		return nil, errors.New("credential issuance has invalid binding or declarations")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if !namePattern.MatchString(name) || seen[name] {
			return nil, errors.New("credential issuance has invalid or duplicate names")
		}
		seen[name] = true
	}
	if err := privateDir(b.root); err != nil {
		return nil, errors.New("credential issuance directory is unavailable or not private")
	}
	d := &Dispatch{dir: filepath.Join(b.root, bind.ID), rec: b.rec, receipt: Receipt{Binding: bind, State: "preparing", References: map[string]string{}, At: time.Now().UTC()}, values: map[string]string{}}
	// The reservation is the preparing receipt: written, durably and
	// exclusively, before any credential is generated.
	if err := b.rec.Reserve(ctx, d.Receipt()); err != nil {
		return nil, err
	}
	for _, name := range names {
		if ctx.Err() != nil {
			return nil, errors.New("credential issuance canceled before dispatch")
		}
		raw := make([]byte, 32)
		if _, err := io.ReadFull(b.random, raw); err != nil {
			return nil, errors.New("credential generation failed before dispatch")
		}
		value := base64.RawURLEncoding.EncodeToString(raw)
		logical := "file://issued/" + bind.ID + "/" + name
		if err := b.store.Set(ctx, logical, strings.NewReader(value)); err != nil {
			return nil, errors.New("credential persistence failed before dispatch")
		}
		// The shared store writes/syncs the file and best-effort syncs its parent.
		// Issuance requires the whole newly created directory chain to be durable.
		for path := filepath.Join(b.root, "secrets", "issued", bind.ID); ; path = filepath.Dir(path) {
			if err := syncDir(path); err != nil {
				return nil, errors.New("credential persistence could not be synced before dispatch")
			}
			if path == b.root {
				break
			}
		}
		resolved, err := b.store.Resolve(ctx, logical)
		if err != nil || resolved.Secret.Reveal() != value {
			return nil, errors.New("credential persistence verification failed before dispatch")
		}
		d.values[name] = value
		d.receipt.References[name] = "file://" + filepath.Join(b.root, "secrets", "issued", bind.ID, name)
	}
	if err := d.record("prepared"); err != nil {
		return nil, err
	}
	return d, nil
}

// Start is written before spawning the privileged operation instance, including
// Init/Load. After this marker, an interrupted outcome is always uncertain.
func (d *Dispatch) Start() error {
	if d == nil || d.receipt.State != "prepared" {
		return errors.New("credential issuance is not prepared")
	}
	return d.record("dispatching")
}
func (d *Dispatch) Finish(success bool) error {
	if d == nil || d.receipt.State != "dispatching" {
		return errors.New("credential issuance is not dispatching")
	}
	state := "uncertain"
	if success {
		state = "completed"
	}
	err := d.record(state)
	clear(d.values)
	return err
}
func (d *Dispatch) record(state string) error {
	r := d.Receipt()
	r.State = state
	r.At = time.Now().UTC()
	if err := d.rec.Record(context.Background(), r); err != nil {
		return err
	}
	d.receipt = r
	return nil
}
func syncDir(path string) error {
	d, err := os.Open(path) //nolint:gosec // host-selected credential directory to fsync
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	return errors.Join(syncErr, closeErr)
}
func privateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	// Refuse rather than chmod an existing store. Ancestors are operator-owned;
	// this is a same-uid custody boundary, not a sandbox against the operator.
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return errors.New("private directory required")
	}
	return syncDir(filepath.Dir(path))
}
