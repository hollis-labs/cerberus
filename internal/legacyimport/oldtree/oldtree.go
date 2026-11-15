// Package oldtree reads the old build's ~/.cerberus as both directions of the migration see it:
// migrate-from reads a snapshot of it (slice 10a) and export-legacy re-reads the live tree after
// writing to it (slice 10b). It holds the parsers of the files the two share (runtime/applied.json,
// the pause markers, the credential-issuance receipts), the audit chain's anchor and check, and
// the file digests, so neither command reaches into the other's unexported code.
//
// It reads only.
package oldtree

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/storage"
)

// Problems collects what a read refuses and what it found absent, in the order found. A caller
// turns them into its own report's refusals and listing.
type Problems struct {
	Refusals []string
	Absent   []string
}

func (p *Problems) refuse(format string, args ...any) {
	p.Refusals = append(p.Refusals, fmt.Sprintf(format, args...))
}

func (p *Problems) absent(rel string) { p.Absent = append(p.Absent, rel) }

// Applied is runtime/applied.json parsed (R9): resource id to "hmac-sha256:<hex>", verbatim.
type Applied struct {
	Present bool
	Digests map[string]string
	MTime   time.Time
	SHA256  string
}

// AppliedRel is the applied digests' file, relative to the old home.
const AppliedRel = "runtime/applied.json"

var digestPattern = regexp.MustCompile(`^hmac-sha256:[0-9a-f]{64}$`)

// ReadApplied parses runtime/applied.json strictly: a JSON object of non-empty ids to digests in
// the old format. Anything else is refused, never guessed at.
func ReadApplied(oldHome string, pr *Problems) Applied {
	p := filepath.Join(oldHome, filepath.FromSlash(AppliedRel))
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		pr.absent(AppliedRel)
		return Applied{}
	}
	if err != nil {
		pr.refuse("runtime/applied.json: %v", err)
		return Applied{}
	}
	if !fi.Mode().IsRegular() {
		pr.refuse("runtime/applied.json is not a regular file")
		return Applied{}
	}
	data, err := os.ReadFile(p) //nolint:gosec // inside the old tree
	if err != nil {
		pr.refuse("runtime/applied.json: %v", err)
		return Applied{}
	}
	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err = dec.Decode(&raw); err != nil || raw == nil || dec.More() {
		pr.refuse("runtime/applied.json is not a JSON object of resource id to digest (%v); fix or remove it in the snapshot, the import does not guess", err)
		return Applied{}
	}
	out := Applied{Present: true, Digests: map[string]string{}, MTime: fi.ModTime()}
	for id, v := range raw {
		s, ok := v.(string)
		switch {
		case strings.TrimSpace(id) == "":
			pr.refuse("runtime/applied.json has an empty resource id")
		case !ok:
			pr.refuse("runtime/applied.json: the digest of %q is a %T, not a string", id, v)
		case !digestPattern.MatchString(s):
			pr.refuse("runtime/applied.json: the digest of %q is not hmac-sha256:<64 hex>", id)
		default:
			out.Digests[id] = s
		}
	}
	out.SHA256, _ = FileSHA256(p)
	return out
}

// PauseMarker is one paused or paused.<id> file (R10).
type PauseMarker struct {
	Name   string
	Target string
	MTime  time.Time
}

// PauseFile is the marker file's name for a pause target: "paused" for storage.AllTargets,
// paused.<target> otherwise.
func PauseFile(target string) string {
	if target == storage.AllTargets {
		return "paused"
	}
	return "paused." + target
}

// ReadPause lists the pause markers at the old home's top level. A directory or link named
// paused* is refused: the old build only ever wrote files.
func ReadPause(oldHome string, pr *Problems) []PauseMarker {
	entries, err := os.ReadDir(oldHome)
	if err != nil {
		pr.refuse("read %s: %v", oldHome, err)
		return nil
	}
	var out []PauseMarker
	for _, e := range entries {
		name := e.Name()
		if name != "paused" && !strings.HasPrefix(name, "paused.") {
			continue
		}
		fi, err := os.Lstat(filepath.Join(oldHome, name))
		if err != nil {
			pr.refuse("%s: %v", name, err)
			continue
		}
		if !fi.Mode().IsRegular() {
			pr.refuse("%s is a %s; a pause marker is a file", name, fi.Mode().Type())
			continue
		}
		target := storage.AllTargets
		if name != "paused" {
			target = strings.TrimPrefix(name, "paused.")
			if target == "" {
				pr.refuse("%s names no target", name)
				continue
			}
		}
		out = append(out, PauseMarker{Name: name, Target: target, MTime: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// Receipt is a credential-issuance receipt file as the old broker wrote it (R11): the fields of
// origin/main's credentialbroker.Receipt in its order (the Binding's id, connector, operation and
// binding_digest, then state, references and at).
type Receipt struct {
	ID            string            `json:"id"`
	Connector     string            `json:"connector"`
	Operation     string            `json:"operation"`
	BindingDigest string            `json:"binding_digest"`
	State         string            `json:"state"`
	References    map[string]string `json:"references"`
	At            time.Time         `json:"at"`
}

// IssuanceID is the shape of a credential-issuance id (32 lowercase hex), the reservation
// directory's name.
var IssuanceID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// ReceiptStates are the receipt files' names, without .json.
var ReceiptStates = map[string]bool{"preparing": true, "prepared": true, "dispatching": true, "completed": true, "uncertain": true}

// Receipts are the parsed receipts, and the reservation directories with none.
type Receipts struct {
	ByFile map[string]storage.Receipt // "<id>/<state>.json"
	Empty  []string
	IDs    int
}

// ReadReceipts parses credential-issuance/<id>/<state>.json, skipping secrets/ (the issued
// values, in place). A name, state or id that does not match its path is refused.
func ReadReceipts(oldHome string, pr *Problems) Receipts {
	out := Receipts{ByFile: map[string]storage.Receipt{}}
	root := filepath.Join(oldHome, "credential-issuance")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		pr.absent("credential-issuance/")
		return out
	}
	if err != nil {
		pr.refuse("credential-issuance: %v", err)
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if name == "secrets" {
			continue
		}
		if !e.IsDir() || !IssuanceID.MatchString(name) {
			pr.refuse("credential-issuance/%s is not a reservation directory (32 lowercase hex)", name)
			continue
		}
		out.IDs++
		files, err := os.ReadDir(filepath.Join(root, name))
		if err != nil {
			pr.refuse("credential-issuance/%s: %v", name, err)
			continue
		}
		if len(files) == 0 {
			// A reservation without a receipt: reported and left in place (10a's rulings).
			out.Empty = append(out.Empty, name)
			continue
		}
		for _, f := range files {
			rel := name + "/" + f.Name()
			state := strings.TrimSuffix(f.Name(), ".json")
			if !f.Type().IsRegular() || !strings.HasSuffix(f.Name(), ".json") || !ReceiptStates[state] {
				pr.refuse("credential-issuance/%s is not a receipt (<state>.json, state one of preparing, prepared, dispatching, completed, uncertain)", rel)
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, name, f.Name())) //nolint:gosec // inside the old tree
			if err != nil {
				pr.refuse("credential-issuance/%s: %v", rel, err)
				continue
			}
			var rc Receipt
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if err = dec.Decode(&rc); err != nil {
				pr.refuse("credential-issuance/%s does not parse as a receipt: %v", rel, err)
				continue
			}
			if rc.ID != name || rc.State != state {
				pr.refuse("credential-issuance/%s records id %q and state %q, which do not match its path", rel, rc.ID, rc.State)
				continue
			}
			if rc.At.IsZero() || rc.Connector == "" || rc.Operation == "" {
				pr.refuse("credential-issuance/%s lacks its time, connector or operation", rel)
				continue
			}
			out.ByFile[rel] = storage.Receipt{IssuanceID: rc.ID, State: rc.State, Connector: rc.Connector, Operation: rc.Operation,
				BindingDigest: rc.BindingDigest, References: rc.References, At: rc.At}
		}
	}
	sort.Strings(out.Empty)
	return out
}

// ReadAnchor is the raw keychain item under account (the audit directory's path), as the old
// build stored it: JSON {seq, hash, file, offset}. ok is false when there is none.
func ReadAnchor(account string) (raw string, head audit.Head, ok bool, err error) {
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

// MemoryAnchor holds a head for a check, and refuses to move it.
type MemoryAnchor struct{ Head audit.Head }

// Load implements audit.Anchor.
func (m MemoryAnchor) Load() (audit.Head, bool, error) { return m.Head, true, nil }

// Store implements audit.Anchor: a memory anchor is only checked against, never moved.
func (MemoryAnchor) Store(audit.Head) error {
	return errors.New("this check reads the chain and never writes to it")
}

// CheckChain runs audit.Check on dir with anchor installed for it, and says whether it verifies:
// no problem, and the chain vouches for its newest record. The anchor is removed afterwards
// (audit.SetAnchor holds one directory per process, so each check installs its own).
func CheckChain(dir string, anchor audit.Anchor) (audit.Checked, string) {
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

// FileSHA256 is "sha256:<hex>" of a file's bytes.
func FileSHA256(p string) (string, error) {
	f, err := os.Open(p) //nolint:gosec // a path inside the old tree or the new data directory
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// PrefixSHA256 is "sha256:<hex>" of the first n bytes of the file at p: the prefix proof of an
// append-only file. A file shorter than n is an error, never a shorter digest.
func PrefixSHA256(p string, n int64) (string, error) {
	if n < 0 {
		return "", fmt.Errorf("%s: a negative prefix length %d", p, n)
	}
	f, err := os.Open(p) //nolint:gosec // a path inside the old tree or the new data directory
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	got, err := io.CopyN(h, f, n)
	if errors.Is(err, io.EOF) || got < n {
		return "", fmt.Errorf("%s is %d bytes, shorter than the %d-byte prefix", p, got, n)
	}
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// BytesSHA256 is "sha256:<hex>" of data.
func BytesSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
