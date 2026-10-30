package hosts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// MaxBackups is how many hosts.yaml.bak-<ts> files a write keeps.
const MaxBackups = 20

// Revision is the optimistic-concurrency token of a hosts file: a digest of
// its bytes. A missing file and an empty one share a revision.
func Revision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// RevisionConflictError is returned when the file changed since the revision
// the writer read. Nothing was written.
type RevisionConflictError struct{ Have, Want string }

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("hosts.yaml changed since you read it (your revision %s, current %s); read it again and redo the change. Nothing was written", e.Want, e.Have)
}

// RevisionRequiredError is returned for a write that carries no revision.
type RevisionRequiredError struct{}

func (RevisionRequiredError) Error() string {
	return "a write needs the revision of hosts.yaml you read (the revision field / If-Match); read it first. Nothing was written"
}

// Editor changes a hosts file safely: one writer at a time, only on the
// revision the writer read, only to a file that validates as a whole, and
// atomically with a backup.
type Editor struct {
	Path string
	Now  func() time.Time
}

// Applied is what a successful Apply did.
type Applied struct {
	Before, After  *File
	RevisionBefore string
	RevisionAfter  string
	Backup         string
	Unchanged      bool
}

func (e *Editor) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator config path
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

func parseOrEmpty(data []byte) (*File, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return &File{Version: FileVersion}, nil
	}
	return Parse(data)
}

// Revision returns the file's current revision.
func (e *Editor) Revision() (string, error) {
	data, err := readFile(e.Path)
	if err != nil {
		return "", err
	}
	return Revision(data), nil
}

// Preview shows what Apply would do, writing nothing and taking no lock: the
// file as it is, the mutation, the validation and the encoding, with the
// revision the result would have.
func (e *Editor) Preview(mutate func(*File) error) (*Applied, error) {
	data, err := readFile(e.Path)
	if err != nil {
		return nil, fmt.Errorf("read hosts config: %w", err)
	}
	before, err := parseOrEmpty(data)
	if err != nil {
		return nil, err
	}
	after, err := parseOrEmpty(data)
	if err != nil {
		return nil, err
	}
	if err := mutate(after); err != nil {
		return nil, err
	}
	if err := Validate(after); err != nil {
		return nil, err
	}
	out, err := encode(after, isJSONStyle(data))
	if err != nil {
		return nil, fmt.Errorf("encode hosts config: %w", err)
	}
	res := &Applied{Before: before, After: after, RevisionBefore: Revision(data), RevisionAfter: Revision(out)}
	if bytes.Equal(out, data) {
		res.Unchanged, res.RevisionAfter = true, res.RevisionBefore
	}
	return res, nil
}

// Apply runs mutate on the file as it is now, validates the complete result
// with the same validator every read uses, and writes it. expect is the
// revision the caller read; it is required. Any failure leaves the file
// byte-identical.
func (e *Editor) Apply(expect string, mutate func(*File) error) (*Applied, error) {
	if expect == "" {
		return nil, RevisionRequiredError{}
	}
	if err := os.MkdirAll(filepath.Dir(e.Path), 0o700); err != nil {
		return nil, fmt.Errorf("prepare hosts directory: %w", err)
	}
	unlock, err := lockFile(e.Path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("lock hosts.yaml: %w", err)
	}
	defer unlock()

	data, err := readFile(e.Path)
	if err != nil {
		return nil, fmt.Errorf("read hosts config: %w", err)
	}
	rev := Revision(data)
	if rev != expect {
		return nil, &RevisionConflictError{Have: rev, Want: expect}
	}
	before, err := parseOrEmpty(data)
	if err != nil {
		return nil, err
	}
	after, err := parseOrEmpty(data)
	if err != nil {
		return nil, err
	}
	if err := mutate(after); err != nil {
		return nil, err
	}
	if err := Validate(after); err != nil {
		return nil, err
	}
	out, err := encode(after, isJSONStyle(data))
	if err != nil {
		return nil, fmt.Errorf("encode hosts config: %w", err)
	}
	// The bytes that will be written must themselves load.
	if _, err := Parse(out); err != nil {
		return nil, fmt.Errorf("the result would not load back (%w); nothing was written", err)
	}
	res := &Applied{Before: before, After: after, RevisionBefore: rev, RevisionAfter: Revision(out)}
	if bytes.Equal(out, data) {
		res.Unchanged = true
		res.RevisionAfter = rev
		return res, nil
	}
	if len(data) > 0 {
		if res.Backup, err = e.backup(data); err != nil {
			return nil, err
		}
	}
	if err := writeAtomic(e.Path, out); err != nil {
		return nil, err
	}
	return res, nil
}

// isJSONStyle reports a file written as JSON (which is valid YAML). The fleet
// inventory script reads and writes hosts.yaml with a JSON library, so a file
// in that style stays in it: a write never changes the format it found.
func isJSONStyle(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(data), []byte("{"))
}

func encode(f *File, asJSON bool) ([]byte, error) {
	if !asJSON {
		return yaml.Marshal(f)
	}
	m, err := toMap(f)
	if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func (e *Editor) backup(data []byte) (string, error) {
	base := e.Path + ".bak-" + e.now().Format("20060102T150405Z")
	path := base
	for i := 1; ; i++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // beside the operator's config
		if err == nil {
			_, werr := f.Write(data)
			serr := f.Sync()
			cerr := f.Close()
			if err := errors.Join(werr, serr, cerr); err != nil {
				return "", fmt.Errorf("write backup: %w", err)
			}
			break
		}
		if !errors.Is(err, os.ErrExist) || i > 100 {
			return "", fmt.Errorf("write backup: %w", err)
		}
		path = fmt.Sprintf("%s-%d", base, i)
	}
	matches, _ := filepath.Glob(e.Path + ".bak-*")
	sort.Strings(matches)
	for len(matches) > MaxBackups {
		_ = os.Remove(matches[0])
		matches = matches[1:]
	}
	return path, nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return fmt.Errorf("replace hosts.yaml: %w", err)
	}
	if d, err := os.Open(dir); err == nil { //nolint:gosec // the config directory
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
