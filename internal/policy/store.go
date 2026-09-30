package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Store is the operator's policy directory, ~/.cerberus/policy:
//
//	*.yaml, providers/*.yaml   working files the operator edits
//	applied.yaml               the snapshot `cerberus policy apply` wrote
//	applied.sha256             its hash, checked on every load
//
// The decision point uses only the applied snapshot, never the working
// files (Decision 10), and a snapshot that no longer matches its hash is
// not used at all (D6).
type Store struct {
	Dir string
	// AuditDir is the audit log the snapshot is checked against, and a
	// mismatch is enforced from: the last verified snapshot recorded there
	// (LastVerified). Empty, the snapshot files are all there is, and a
	// mismatch enforces everything.
	AuditDir string
}

const (
	appliedName = "applied.yaml"
	hashName    = "applied.sha256"
)

// Snapshot identities for a result.
const (
	SnapshotBaseline = "baseline"
	SnapshotMismatch = "mismatch"
)

// Hash is the snapshot hash of an applied file's bytes.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// WorkingFiles are the operator's policy files, in a stable order: the
// directory's own, then providers/.
func (s Store) WorkingFiles() ([]string, error) {
	var out []string
	for _, pattern := range []string{filepath.Join(s.Dir, "*.yaml"), filepath.Join(s.Dir, "providers", "*.yaml")} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, m := range matches {
			if filepath.Base(m) != appliedName {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// LoadWorking merges the working files. Problems name the file they are in.
func (s Store) LoadWorking() (File, []string, error) {
	paths, err := s.WorkingFiles()
	if err != nil {
		return File{}, nil, err
	}
	var files []File
	var problems []string
	for _, path := range paths {
		f, err := readFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", filepath.Base(path), err))
			continue
		}
		for _, p := range f.Validate() {
			problems = append(problems, fmt.Sprintf("%s: %s", relName(s.Dir, path), p))
		}
		files = append(files, f)
	}
	return Merge(files...), problems, nil
}

// LoadStatus is what loading the applied snapshot found.
type LoadStatus struct {
	// Snapshot is the applied hash, SnapshotBaseline when nothing is
	// applied, or SnapshotMismatch.
	Snapshot string
	// Recorded and Found are the two hashes of a mismatch.
	Recorded, Found string
	Problem         string
	// Enforcement is what a mismatch enforces, as a person reads it: the
	// last verified scopes and when they were applied, or everything.
	Enforcement string
}

// Mismatch reports an applied snapshot that failed its hash check.
func (l LoadStatus) Mismatch() bool { return l.Snapshot == SnapshotMismatch }

// Load returns the decision point for the applied snapshot. With nothing
// applied, it is the baseline. With a snapshot that does not match its
// recorded hash, or does not parse, it is the baseline too — the strict
// known-good — and every result names the mismatch. It never uses a file it
// cannot vouch for.
func (s Store) Load() (*Evaluator, LoadStatus) {
	data, err := os.ReadFile(filepath.Join(s.Dir, appliedName)) //nolint:gosec // the operator's own policy directory
	if errors.Is(err, os.ErrNotExist) {
		return BaselineOnly(SnapshotBaseline), LoadStatus{Snapshot: SnapshotBaseline}
	}
	if err != nil {
		return BaselineOnly(SnapshotMismatch), LoadStatus{Snapshot: SnapshotMismatch, Problem: err.Error()}
	}
	found := Hash(data)
	recordedRaw, err := os.ReadFile(filepath.Join(s.Dir, hashName)) //nolint:gosec // the operator's own policy directory
	recorded := strings.TrimSpace(string(recordedRaw))
	if err != nil || recorded != found {
		return BaselineOnly(SnapshotMismatch), LoadStatus{Snapshot: SnapshotMismatch, Recorded: recorded, Found: found,
			Problem: "applied.yaml does not match the hash `cerberus policy apply` recorded"}
	}
	f, err := decodeFile(data)
	if err != nil {
		return BaselineOnly(SnapshotMismatch), LoadStatus{Snapshot: SnapshotMismatch, Recorded: recorded, Found: found, Problem: err.Error()}
	}
	if problems := f.Validate(); len(problems) > 0 {
		return BaselineOnly(SnapshotMismatch), LoadStatus{Snapshot: SnapshotMismatch, Recorded: recorded, Found: found, Problem: strings.Join(problems, "; ")}
	}
	return NewEvaluator(f, found), LoadStatus{Snapshot: found}
}

// LoadVerified is Load, checked against the audit log (M3). The snapshot's
// own hash file vouches only for itself, so the snapshot in use must also be
// the one the newest apply the log vouches for wrote (LastVerified). One
// that is not — edited with its hash file, deleted, or failing its hash
// check — is a mismatch, and a mismatch keeps the last verified snapshot:
// its rules, rates, breaker and enforcement, from the snapshot the apply
// recorded. Where the log holds only its enforcement, that is enforced over
// the baseline; where it vouches for nothing, everything is enforced.
func (s Store) LoadVerified() (*Evaluator, LoadStatus) {
	ev, status := s.Load()
	v, history := LastVerified(s.AuditDir)
	switch history {
	case NoApplies:
		if !status.Mismatch() {
			return ev, status
		}
	case UnverifiedApplies:
		if !status.Mismatch() {
			status = LoadStatus{Snapshot: SnapshotMismatch, Found: foundOf(status),
				Problem: "the audit log vouches for no `cerberus policy apply` (its chain has a problem before every one), so this snapshot cannot be checked; run `cerberus policy apply` again once the chain is reanchored"}
		}
		ev = BaselineOnly(SnapshotMismatch)
		status.Enforcement = "snapshot mismatch: enforcing everything (the audit log vouches for no apply)"
		ev.fallbackNote = status.Enforcement
		return ev, status
	case VerifiedApply:
		if !status.Mismatch() && (v.Hash == "" || foundOf(status) == v.Hash) {
			return ev, status
		}
		if !status.Mismatch() {
			problem := "applied.yaml is not the snapshot the last `cerberus policy apply` wrote"
			if status.Snapshot == SnapshotBaseline {
				problem = "applied.yaml is missing, but `cerberus policy apply` wrote one"
			}
			status = LoadStatus{Snapshot: SnapshotMismatch, Recorded: v.Hash, Found: foundOf(status), Problem: problem}
		}
		if f, ok := verifiedFile(v); ok {
			status.Enforcement = fmt.Sprintf("snapshot mismatch: enforcing the last verified snapshot, applied %s: its rules, and %s", v.Time.Local().Format(time.RFC3339), f.EnforcementOf().Summary())
			e := f.EnforcementOf()
			return &Evaluator{file: f, snapshot: SnapshotMismatch, fallback: &e, fallbackNote: status.Enforcement}, status
		}
		ev = BaselineOnly(SnapshotMismatch)
		e := v.Enforcement
		ev.fallback = &e
		status.Enforcement = fmt.Sprintf("snapshot mismatch: enforcing the last verified enforcement, from %s: %s", v.Time.Local().Format(time.RFC3339), e.Summary())
		ev.fallbackNote = status.Enforcement
		return ev, status
	}
	status.Enforcement = "snapshot mismatch: enforcing everything (the last verified enforcement cannot be read from the audit log)"
	ev.fallbackNote = status.Enforcement
	return ev, status
}

// foundOf is the snapshot the files hold: its hash, or "none".
func foundOf(status LoadStatus) string {
	switch status.Snapshot {
	case SnapshotBaseline:
		return "none"
	case SnapshotMismatch:
		return status.Found
	}
	return status.Snapshot
}

// verifiedFile is the snapshot an apply recorded, when it is the one whose
// hash the apply named and it still validates.
func verifiedFile(v Verified) (File, bool) {
	if len(v.Snapshot) == 0 || Hash(v.Snapshot) != v.Hash {
		return File{}, false
	}
	f, err := decodeFile(v.Snapshot)
	if err != nil || len(f.Validate()) > 0 {
		return File{}, false
	}
	return f, true
}

// Encode is a file as the snapshot writes it.
func Encode(f File) ([]byte, error) {
	f.Version = FileVersion
	return yaml.Marshal(f)
}

// Apply writes f as the applied snapshot and records its hash. Both files
// are written whole and renamed into place, the hash last, so a crash
// leaves either the old snapshot or a mismatch, never an unchecked file.
func (s Store) Apply(f File) (string, error) {
	data, err := Encode(f)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	hash := Hash(data)
	if err := writeAtomic(filepath.Join(s.Dir, appliedName), data); err != nil {
		return "", err
	}
	if err := writeAtomic(filepath.Join(s.Dir, hashName), []byte(hash+"\n")); err != nil {
		return "", err
	}
	return hash, nil
}

// WriteWorking writes a working file under the directory, for a plugin's
// accepted suggested policy.
func (s Store) WriteWorking(rel string, f File) error {
	data, err := Encode(f)
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// ReadWorking reads one working file; missing is ok with the zero File.
func (s Store) ReadWorking(rel string) (File, bool, error) {
	f, err := readFile(filepath.Join(s.Dir, rel))
	if errors.Is(err, os.ErrNotExist) {
		return File{}, false, nil
	}
	return f, err == nil, err
}

func readFile(path string) (File, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the operator's own policy directory
	if err != nil {
		return File{}, err
	}
	f, err := decodeFile(data)
	if err != nil {
		return File{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return f, nil
}

// decodeFile reads a policy file strictly: a key the file format does not
// have is an error. Read loosely, a misspelled key is dropped without a
// word, and a dropped key widens a rule: `ops` misspelled on an allow rule
// makes it allow every operation. An empty file is an empty File.
func decodeFile(data []byte) (File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return File{}, err
	}
	return f, nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func relName(dir, path string) string {
	if rel, err := filepath.Rel(dir, path); err == nil {
		return rel
	}
	return filepath.Base(path)
}

// Reloading is a decision point over the applied snapshot that notices a
// new `cerberus policy apply`, or a changed file, on the next decision: it
// re-checks the two files' modification times on each call and reloads when
// they move. onLoad hears every load, so a mismatch can be recorded.
type Reloading struct {
	store  Store
	onLoad func(LoadStatus)

	mu      sync.Mutex
	stamp   string
	current *Evaluator
}

// NewReloading loads the applied snapshot now and on every change.
func NewReloading(store Store, onLoad func(LoadStatus)) *Reloading {
	r := &Reloading{store: store, onLoad: onLoad}
	r.reloadIfChanged()
	return r
}

var _ PDP = (*Reloading)(nil)

// Authorize implements PDP.
func (r *Reloading) Authorize(req Request) Result {
	return r.reloadIfChanged().Authorize(req)
}

// GlobalPosture implements PDP.
func (r *Reloading) GlobalPosture() string {
	return r.reloadIfChanged().GlobalPosture()
}

// File is the applied snapshot's file, as it reads now.
func (r *Reloading) File() File { return r.reloadIfChanged().File() }

// Enforced reports whether req is enforced under the applied snapshot.
func (r *Reloading) Enforced(req Request) (bool, string) { return r.reloadIfChanged().Enforced(req) }

// Enforcement is what the applied snapshot enforces, and a note when it is
// a mismatch's fallback.
func (r *Reloading) Enforcement() (Enforcement, string) { return r.reloadIfChanged().Enforcement() }

func (r *Reloading) reloadIfChanged() *Evaluator {
	stamp := r.stampNow()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil && stamp == r.stamp {
		return r.current
	}
	pdp, status := r.store.LoadVerified()
	r.current, r.stamp = pdp, stamp
	if r.onLoad != nil {
		r.onLoad(status)
	}
	return r.current
}

func (r *Reloading) stampNow() string {
	var b strings.Builder
	for _, name := range []string{appliedName, hashName} {
		if info, err := os.Stat(filepath.Join(r.store.Dir, name)); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", name, info.Size(), info.ModTime().UnixNano())
		} else {
			b.WriteString(name + ":absent;")
		}
	}
	return b.String()
}
