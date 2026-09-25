package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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
type Store struct{ Dir string }

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
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return BaselineOnly(SnapshotMismatch), LoadStatus{Snapshot: SnapshotMismatch, Recorded: recorded, Found: found, Problem: err.Error()}
	}
	if problems := f.Validate(); len(problems) > 0 {
		return BaselineOnly(SnapshotMismatch), LoadStatus{Snapshot: SnapshotMismatch, Recorded: recorded, Found: found, Problem: strings.Join(problems, "; ")}
	}
	return NewEvaluator(f, found), LoadStatus{Snapshot: found}
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
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
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

func (r *Reloading) reloadIfChanged() *Evaluator {
	stamp := r.stampNow()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil && stamp == r.stamp {
		return r.current
	}
	pdp, status := r.store.Load()
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
