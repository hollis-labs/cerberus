// Package manifest is the D3 manifest (docs/substrate-port-plan.md, section 5): taken of the old
// tree with old in standby, before the window opens, it records every old file export-legacy
// could touch (sha256, or absent) and main.db's logical state (counts, newest times, the
// sequence and the ordered digest of the five host tables, never the file's bytes), and the
// audit anchor's head. export-legacy refuses a rollback unless the old tree still matches it,
// and migrate-from checks its snapshot against it.
//
// Excluded: standby.d/ (old's own record of the window); registry.yaml, hosts.yaml,
// hosts.history.jsonl and its rotations, and plugin-connectors.json (they stay in place in the
// adopted root and new edits them while it runs; old reads the same format, so a rollback keeps
// new's edits rather than refusing on them); the cerberus-substrate unit, *.lock, cerberus.log,
// the rest of the adopted root (apps, plugins, tokens), and main.db's -wal and -shm bytes.
//
// It reads only: files are hashed, the keychain item is read, and the database is opened
// read-only (immutable for a backup; plain read-only for the live file, which reads through its
// WAL and never checkpoints).
package manifest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/legacyimport/hostdump"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

// Version is the manifest format.
const Version = 1

// Manifest is the D3 manifest.
type Manifest struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	// OldHome and OldDB are where it was taken (informational: a check compares by relative
	// path, so a snapshot of the tree checks against it).
	OldHome string `json:"old_home"`
	OldDB   string `json:"old_db"`
	// Files are every file in scope by path relative to the old home: sha256 and size, or
	// absent, so a file that appears later is a mismatch.
	Files map[string]File `json:"files"`
	// Dirs are the credential-issuance reservation directories (an empty one is a reservation
	// without a receipt).
	Dirs []string `json:"dirs"`
	// Anchor is the audit chain's anchored head in the keychain.
	Anchor Anchor `json:"anchor"`
	// DB is main.db's logical state.
	DB hostdump.State `json:"db"`
	// Excluded documents what is deliberately not covered.
	Excluded []string `json:"excluded"`
	// BodySHA256 is the sha256 of the manifest with this field empty.
	BodySHA256 string `json:"body_sha256"`
}

// File is one file's state.
type File struct {
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Absent bool   `json:"absent,omitempty"`
}

// Anchor is the keychain item's head (absent when there is none).
type Anchor struct {
	Account string          `json:"account"`
	Absent  bool            `json:"absent,omitempty"`
	Head    json.RawMessage `json:"head,omitempty"`
}

// Options say what to take or check.
type Options struct {
	OldHome string
	OldDB   string
	// AnchorAccount is the keychain account of the old chain's anchor; empty is OldHome/audit,
	// right for the live tree (the old build's account is its audit directory's path).
	AnchorAccount string
	// Live opens the database read-only through its WAL (the live main.db); otherwise it is
	// opened immutable (a backup nothing writes).
	Live bool
	Now  func() time.Time
}

// fixedFiles are the files export-legacy could touch that have one name each. Absent ones are
// recorded absent.
var fixedFiles = []string{
	"audit/.digest_key",
	"approvals/events.jsonl",
	"state/hosts.json",
	"state/overview_snapshots.json",
	"runtime/applied.json",
	"paused",
	"brakes/events.jsonl",
}

// excluded is the documented exclusion list.
var excluded = []string{
	"standby.d/", "registry.yaml", "hosts.yaml", "hosts.history.jsonl*", "plugin-connectors.json",
	"the cerberus-substrate unit file", "*.lock", "cerberus.log",
	"the adopted root (apps/, plugins*, outpost-tokens/, events-tokens/, secrets/, transfers/, credential-issuance/secrets/)",
	"main.db-wal and main.db-shm bytes (main.db is recorded logically)",
}

func (o Options) account() string {
	if o.AnchorAccount != "" {
		return o.AnchorAccount
	}
	return filepath.Join(o.OldHome, "audit")
}

// Take builds the manifest of the tree and database as they are now.
func Take(ctx context.Context, o Options) (*Manifest, error) {
	if o.OldHome == "" || o.OldDB == "" {
		return nil, errors.New("manifest: the old home and the old database are both required")
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	m := &Manifest{Version: Version, CreatedAt: now().UTC(), OldHome: o.OldHome, OldDB: o.OldDB,
		Files: map[string]File{}, Dirs: []string{}, Excluded: excluded}
	var err error
	if m.Files, m.Dirs, err = files(o.OldHome); err != nil {
		return nil, err
	}
	if m.Anchor, err = readAnchor(o.account()); err != nil {
		return nil, err
	}
	if m.DB, err = dbState(ctx, o.OldDB, o.Live); err != nil {
		return nil, err
	}
	return m, nil
}

// files hashes every file in scope.
func files(home string) (map[string]File, []string, error) {
	out := map[string]File{}
	add := func(rel string) error {
		p := filepath.Join(home, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			out[rel] = File{Absent: true}
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("manifest: %s is a %s, not a file", rel, fi.Mode().Type())
		}
		sum, err := sha256File(p)
		if err != nil {
			return err
		}
		out[rel] = File{SHA256: sum, Size: fi.Size()}
		return nil
	}
	rels := append([]string(nil), fixedFiles...)
	for _, pattern := range []string{"audit/*.jsonl", "paused.*", "credential-issuance/*/*.json"} {
		matches, err := filepath.Glob(filepath.Join(home, filepath.FromSlash(pattern)))
		if err != nil {
			return nil, nil, err
		}
		for _, m := range matches {
			rel, _ := filepath.Rel(home, m)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "credential-issuance/secrets/") {
				continue
			}
			rels = append(rels, rel)
		}
	}
	for _, rel := range rels {
		if err := add(rel); err != nil {
			return nil, nil, err
		}
	}
	dirs := []string{}
	entries, err := os.ReadDir(filepath.Join(home, "credential-issuance"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != "secrets" {
			dirs = append(dirs, "credential-issuance/"+e.Name())
		}
	}
	sort.Strings(dirs)
	return out, dirs, nil
}

func readAnchor(account string) (Anchor, error) {
	a := Anchor{Account: account}
	raw, err := secrets.KeyringGet(auditchain.AnchorService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		a.Absent = true
		return a, nil
	}
	if err != nil {
		return a, fmt.Errorf("manifest: read the audit anchor for %s: %w", account, err)
	}
	if !json.Valid([]byte(raw)) {
		return a, fmt.Errorf("manifest: the audit anchor for %s is not JSON", account)
	}
	a.Head = json.RawMessage(raw)
	return a, nil
}

func dbState(ctx context.Context, path string, live bool) (hostdump.State, error) {
	var db *sql.DB
	var err error
	if live {
		db, err = hostdump.OpenLive(path)
	} else {
		db, err = hostdump.Open(path)
	}
	if err != nil {
		return hostdump.State{}, fmt.Errorf("manifest: open %s read-only: %w", path, err)
	}
	defer func() { _ = db.Close() }()
	st, err := hostdump.ReadState(ctx, db, "")
	if err != nil {
		return hostdump.State{}, fmt.Errorf("manifest: read %s: %w", path, err)
	}
	return st, nil
}

// Seal sets BodySHA256 and returns the manifest's bytes.
func (m *Manifest) Seal() ([]byte, error) {
	m.BodySHA256 = ""
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	m.BodySHA256 = "sha256:" + hex.EncodeToString(sum[:])
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Write takes the manifest and writes it to out, a new file (0600). An existing file is
// refused: a manifest is taken once per window.
func Write(ctx context.Context, o Options, out string) (*Manifest, error) {
	m, err := Take(ctx, o)
	if err != nil {
		return nil, err
	}
	data, err := m.Seal()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the operator's chosen output
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	_, werr := f.Write(data)
	if err = errors.Join(werr, f.Sync(), f.Close()); err != nil {
		return nil, err
	}
	return m, nil
}

// KeptRel is where migrate-from keeps the manifest it checked its snapshot against, relative to
// the new data directory: the handoff export-legacy reverses.
const KeptRel = "migration/d3-manifest.json"

// LoadKept loads the manifest migrate-from kept in dataDir (Load: its body digest is checked).
func LoadKept(dataDir string) (*Manifest, string, error) {
	p := filepath.Join(dataDir, filepath.FromSlash(KeptRel))
	if _, err := os.Lstat(p); err != nil {
		return nil, "", fmt.Errorf("manifest: migrate-from kept no D3 manifest at %s (%w); a rollback needs the handoff it recorded", p, err)
	}
	return Load(p)
}

// Load reads a manifest and checks its body digest. It returns the file's sha256 as well.
func Load(path string) (*Manifest, string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the operator's manifest
	if err != nil {
		return nil, "", fmt.Errorf("manifest: %w", err)
	}
	var m Manifest
	if err = json.Unmarshal(data, &m); err != nil {
		return nil, "", fmt.Errorf("manifest %s does not parse: %w", path, err)
	}
	if m.Version != Version {
		return nil, "", fmt.Errorf("manifest %s is version %d; this build reads %d", path, m.Version, Version)
	}
	want := m.BodySHA256
	if _, err = m.Seal(); err != nil {
		return nil, "", err
	}
	if m.BodySHA256 != want {
		return nil, "", fmt.Errorf("manifest %s has been edited: its body digest is %s, it records %s", path, m.BodySHA256, want)
	}
	sum := sha256.Sum256(data)
	return &m, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p) //nolint:gosec // a file of the old tree, read only
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
