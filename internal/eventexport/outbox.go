package eventexport

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
)

// SourceRef says which source record an entry was derived from: the source
// ("audit", "approval", "scheduling") and the record's sequence there. It is
// informational; deduplication is by Entry.Key. Synthetic entries (gap and stop
// alerts) have a source and no sequence.
type SourceRef struct {
	Source string `json:"source"`
	Seq    uint64 `json:"seq,omitempty"`
}

// Entry is one outbox line: an envelope, the key that makes appending it
// idempotent, and where it came from. Seq is the outbox's own producer-wide
// counter and is the envelope's sequence.
type Entry struct {
	Seq      uint64    `json:"seq"`
	Prev     string    `json:"prev"`
	Key      string    `json:"key"`
	Src      SourceRef `json:"src"`
	Envelope Envelope  `json:"env"`
}

// Outbox is an append-only, ordered log of envelopes. It is shaped like
// streamhub.Log (Append, After, Head) so it can be replaced by the durable
// SQLite log without touching the mappers.
type Outbox interface {
	// Append stores e under key and returns the stored entry. When key is
	// already in the outbox nothing is written and the existing entry is
	// returned with created false.
	Append(key string, src SourceRef, e Envelope) (entry Entry, created bool, err error)
	// After returns up to limit entries with a sequence greater than seq.
	After(seq uint64, limit int) ([]Entry, error)
	// Head is the newest entry; ok is false when the outbox is empty.
	Head() (entry Entry, ok bool, err error)
	// Generation names this outbox's sequence namespace.
	Generation() uint64
}

// ErrRefused is returned when an outbox is not in a state it can safely
// extend. The exporter does not start on it: it never guesses its way past an
// inconsistent ledger, because reusing a sequence would let two different
// envelopes share an id in the relay's eyes.
var ErrRefused = errors.New("eventexport: the outbox is not consistent and will not be extended")

const (
	outboxFile     = "outbox.jsonl"
	generationFile = "generation"
)

// FileOutbox is the JSONL implementation of Outbox. One process (the daemon)
// owns it.
type FileOutbox struct {
	dir        string
	generation uint64

	fs     outboxFS
	broken error // set when a failed append could not be rolled back; every later Append fails

	mu   sync.Mutex
	head Entry
	n    uint64
	keys map[string]uint64 // key -> seq
	sum  string            // hash of the last line, for the next line's Prev
}

// OpenFileOutbox opens (creating if needed) the outbox in dir.
//
// A final line that is torn, as after a crash mid-append, is truncated away and
// the outbox carries on. Anything else wrong refuses (ErrRefused): a line that
// does not parse before the end, a sequence that is not contiguous, a broken
// prev link, a duplicate key, or entries with no generation file. In particular
// an outbox is never given a fresh generation while it still holds entries: a
// restored or copied directory is refused, and the operator moves it aside so a
// new outbox starts with a new generation and a new id space.
func OpenFileOutbox(dir string) (*FileOutbox, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("eventexport: create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory: owner-only
		return nil, fmt.Errorf("eventexport: restrict %s: %w", dir, err)
	}
	o := &FileOutbox{dir: dir, keys: map[string]uint64{}, fs: osFS{}}
	path := filepath.Join(dir, outboxFile)
	data, readErr := os.ReadFile(path) //nolint:gosec // the outbox's own file
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("eventexport: read %s: %w", path, readErr)
	}
	generation, genErr := readGeneration(dir)
	switch {
	case genErr == nil:
		o.generation = generation
	case errors.Is(genErr, os.ErrNotExist) && len(bytes.TrimSpace(data)) == 0:
		fresh, err := newGeneration()
		if err != nil {
			return nil, err
		}
		if err := writeGeneration(dir, fresh); err != nil {
			return nil, err
		}
		o.generation = fresh
	case errors.Is(genErr, os.ErrNotExist):
		return nil, fmt.Errorf("%w: %s holds entries but its generation file is missing", ErrRefused, dir)
	default:
		return nil, fmt.Errorf("%w: %w", ErrRefused, genErr)
	}
	if readErr == nil {
		if err := os.Chmod(path, 0o600); err != nil { //nolint:gosec // owner-only
			return nil, fmt.Errorf("eventexport: restrict %s: %w", path, err)
		}
	}
	good, err := o.load(data)
	if err != nil {
		return nil, err
	}
	if good < int64(len(data)) {
		if err := os.Truncate(path, good); err != nil {
			return nil, fmt.Errorf("eventexport: truncate torn tail of %s: %w", path, err)
		}
	}
	return o, nil
}

// load replays data into the index and returns the length of the good prefix.
func (o *FileOutbox) load(data []byte) (int64, error) {
	var good int64
	r := bufio.NewReader(bytes.NewReader(data))
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 && errors.Is(err, io.EOF) {
			return good, nil
		}
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		var e Entry
		parsed := complete && json.Unmarshal(bytes.TrimSpace(line), &e) == nil
		if !parsed {
			if errors.Is(err, io.EOF) { // only the last line may be torn
				return good, nil //nolint:nilerr // a torn last line is repaired, not an error
			}
			return 0, fmt.Errorf("%w: a line before the end of the outbox does not parse", ErrRefused)
		}
		if e.Seq != o.n+1 || e.Prev != o.sum {
			return 0, fmt.Errorf("%w: entry %d does not follow entry %d", ErrRefused, e.Seq, o.n)
		}
		if _, dup := o.keys[e.Key]; dup {
			return 0, fmt.Errorf("%w: key %q appears twice", ErrRefused, e.Key)
		}
		if e.Envelope.Generation != o.generation {
			return 0, fmt.Errorf("%w: entry %d is from generation %d, not %d", ErrRefused, e.Seq, e.Envelope.Generation, o.generation)
		}
		o.keys[e.Key] = e.Seq
		o.head, o.n, o.sum = e, e.Seq, lineSum(bytes.TrimSpace(line))
		good += int64(len(line))
	}
}

func lineSum(line []byte) string {
	sum := sha256.Sum256(line)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Generation implements Outbox.
func (o *FileOutbox) Generation() uint64 { return o.generation }

// Append implements Outbox.
func (o *FileOutbox) Append(key string, src SourceRef, e Envelope) (Entry, bool, error) {
	if key == "" {
		return Entry{}, false, errors.New("eventexport: an outbox entry needs a key")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if seq, ok := o.keys[key]; ok {
		existing, err := o.entry(seq)
		return existing, false, err
	}
	e.SchemaVersion = runtimeevents.SchemaVersion
	if e.App == "" {
		e.App = AppName
	}
	if e.ID == "" {
		e.ID = newEventID()
	}
	e.Sequence, e.Generation = o.n+1, o.generation
	entry := Entry{Seq: o.n + 1, Prev: o.sum, Key: key, Src: src, Envelope: e}
	line, err := json.Marshal(entry)
	if err != nil {
		return Entry{}, false, err
	}
	if err := o.write(append(line, '\n')); err != nil {
		return Entry{}, false, err
	}
	o.keys[key], o.head, o.n, o.sum = entry.Seq, entry, entry.Seq, lineSum(line)
	return entry, true, nil
}

// write appends one complete line durably. On any failure it truncates the file
// back to its size before the attempt, so a line that reached the file but was
// not acknowledged (a short write, a failed sync or close) is not left behind to
// be duplicated by the retry, to corrupt the line after it, or to make the
// outbox refuse to open. If even the rollback fails the outbox is marked broken:
// every later Append fails rather than build on a file in an unknown state.
func (o *FileOutbox) write(line []byte) error {
	if o.broken != nil {
		return o.broken
	}
	path := filepath.Join(o.dir, outboxFile)
	var size int64
	created := false
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	} else if errors.Is(err, os.ErrNotExist) {
		created = true
	} else {
		return fmt.Errorf("eventexport: stat outbox: %w", err)
	}
	f, err := o.fs.OpenAppend(path)
	if err != nil {
		return fmt.Errorf("eventexport: open outbox: %w", err)
	}
	var werr error
	if _, werr = f.Write(line); werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil && created {
		werr = o.fs.SyncDir(o.dir)
	}
	if werr == nil {
		return nil
	}
	if terr := o.fs.Truncate(path, size); terr != nil {
		o.broken = fmt.Errorf("%w: an append failed (%w) and could not be rolled back (%w)", ErrRefused, werr, terr)
		return o.broken
	}
	return fmt.Errorf("eventexport: append: %w", werr)
}

// outboxFS is the file system the outbox writes through; tests replace it to
// inject failures.
type outboxFS interface {
	OpenAppend(path string) (appendFile, error)
	Truncate(path string, size int64) error
	SyncDir(dir string) error
}

type appendFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type osFS struct{}

func (osFS) OpenAppend(path string) (appendFile, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // the outbox's own file
}
func (osFS) Truncate(path string, size int64) error { return os.Truncate(path, size) }
func (osFS) SyncDir(dir string) error               { return syncDir(dir) }

// syncDir makes a creation or rename in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // the outbox's own directory
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	if closeErr := d.Close(); syncErr == nil {
		syncErr = closeErr
	}
	return syncErr
}

// entry reads one stored entry by sequence.
func (o *FileOutbox) entry(seq uint64) (Entry, error) {
	entries, err := o.scan(seq-1, 1)
	if err != nil || len(entries) == 0 {
		return Entry{}, errors.Join(err, fmt.Errorf("eventexport: entry %d not found", seq))
	}
	return entries[0], nil
}

// After implements Outbox.
func (o *FileOutbox) After(seq uint64, limit int) ([]Entry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.scan(seq, limit)
}

func (o *FileOutbox) scan(after uint64, limit int) ([]Entry, error) {
	f, err := os.Open(filepath.Join(o.dir, outboxFile)) //nolint:gosec // the outbox's own file
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Entry
	r := bufio.NewReader(f)
	for {
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var e Entry
			if json.Unmarshal(bytes.TrimSpace(line), &e) != nil {
				return out, fmt.Errorf("%w: an outbox line does not parse", ErrRefused)
			}
			if e.Seq > after {
				out = append(out, e)
				if limit > 0 && len(out) >= limit {
					return out, nil
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return out, nil
			}
			return out, readErr
		}
	}
}

// Head implements Outbox.
func (o *FileOutbox) Head() (Entry, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.head, o.n > 0, nil
}

// now is the clock for envelopes the outbox stamps itself.
func (o *FileOutbox) now() time.Time { return time.Now().UTC() }

// Has reports whether key is in the outbox.
func (o *FileOutbox) Has(key string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.keys[key]
	return ok
}

func newGeneration() (uint64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("eventexport: no randomness: %w", err)
	}
	// Non-zero and below 2^63 so every JSON reader keeps it exact.
	if g := binary.BigEndian.Uint64(b[:]) >> 1; g != 0 {
		return g, nil
	}
	return 1, nil
}

func readGeneration(dir string) (uint64, error) {
	data, err := os.ReadFile(filepath.Join(dir, generationFile)) //nolint:gosec // the outbox's own file
	if err != nil {
		return 0, err
	}
	var g uint64
	if _, err := fmt.Sscanf(string(bytes.TrimSpace(data)), "%d", &g); err != nil || g == 0 {
		return 0, errors.New("the generation file is unreadable")
	}
	return g, nil
}

func writeGeneration(dir string, g uint64) error {
	tmp := filepath.Join(dir, generationFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the outbox's own file
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(f, "%d\n", g); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(dir, generationFile)); err != nil {
		return err
	}
	return syncDir(dir)
}
