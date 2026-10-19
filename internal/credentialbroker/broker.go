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

// Broker is safe to share: exclusive directory creation arbitrates concurrent
// requests across processes. A reservation is never deleted or automatically
// replayed, including when preparation or a later operation fails.
type Broker struct {
	root   string
	store  *shared.Resolver
	random io.Reader
}

func New(root string) *Broker {
	return &Broker{root: root, store: shared.New(shared.WithStateDir(filepath.Join(root, "secrets"))), random: rand.Reader}
}

// Dispatch deliberately cannot be serialized or constructed by a wire caller.
// The returned values belong only in private Init config and redaction scopes.
type Dispatch struct {
	receipt Receipt
	values  map[string]string
	dir     string
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
	dir := filepath.Join(b.root, bind.ID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, errors.New("credential issuance ID is already reserved or unavailable; inspect its durable record before retrying")
	}
	if err := syncDir(b.root); err != nil {
		return nil, errors.New("credential issuance reservation could not be synced")
	}
	d := &Dispatch{dir: dir, receipt: Receipt{Binding: bind, State: "preparing", References: map[string]string{}, At: time.Now().UTC()}, values: map[string]string{}}
	if err := d.record("preparing"); err != nil {
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
	data, err := json.Marshal(r)
	if err != nil {
		return errors.New("credential issuance record encoding failed")
	}
	f, err := os.OpenFile(filepath.Join(d.dir, state+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // dir is a reserved host path; state is an internal lifecycle constant
	if err != nil {
		return errors.New("credential issuance record could not be created")
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("credential issuance record could not be persisted")
	}
	if syncDir(d.dir) != nil {
		return errors.New("credential issuance record directory could not be synced")
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
