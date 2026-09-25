package audit

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

// Sink takes records. Write is synchronous: when it returns nil the record
// is durable, and when it returns an error the caller must treat the record
// as not written. The sink assigns Seq, Time, PrevHash and Hash.
type Sink interface {
	Write(rec Record) (Record, error)
	// Digest is the keyed digest of an operation's arguments.
	Digest(args any) string
}

// FileSink appends records to monthly JSONL files under one directory:
// YYYY-MM.jsonl, mode 0600, in a 0700 directory.
//
// One process may hold several FileSinks and several processes may share a
// directory — the daemon and an in-process CLI both write here — so every
// append runs under an exclusive flock on the directory's lock file and
// re-reads the chain tail when another writer has appended since. Within a
// process a mutex orders writers, so the flock is only ever contended
// across processes. Every append is fsynced before Write returns.
type FileSink struct {
	dir string
	now func() time.Time
	key []byte

	mu sync.Mutex
	// Cached tail, valid while the file is the size this sink left it.
	file string
	size int64
	seq  uint64
	last string
}

const (
	dirMode  = 0o700
	fileMode = 0o600
	lockName = ".lock"
	keyName  = ".digest_key"
)

// OpenFileSink opens (creating if needed) the audit directory dir.
func OpenFileSink(dir string) (*FileSink, error) {
	return openFileSink(dir, time.Now)
}

func openFileSink(dir string, now func() time.Time) (*FileSink, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("audit: create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return nil, fmt.Errorf("audit: restrict %s: %w", dir, err)
	}
	key, err := loadOrCreateKey(filepath.Join(dir, keyName))
	if err != nil {
		return nil, err
	}
	return &FileSink{dir: dir, now: now, key: key}, nil
}

// Dir is the directory the sink writes to.
func (s *FileSink) Dir() string { return s.dir }

func loadOrCreateKey(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil && len(data) == 32 { //nolint:gosec // the audit directory's own key file
		return data, nil
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(randReader, key); err != nil {
		return nil, fmt.Errorf("audit: digest key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode) //nolint:gosec // as above
	if errors.Is(err, os.ErrExist) {
		// Another writer created it first; use theirs.
		data, readErr := os.ReadFile(path) //nolint:gosec // as above
		if readErr != nil || len(data) != 32 {
			return nil, fmt.Errorf("audit: digest key %s is unreadable", path)
		}
		return data, nil
	}
	if err != nil {
		return nil, fmt.Errorf("audit: digest key: %w", err)
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.Write(key); err != nil {
		return nil, fmt.Errorf("audit: digest key: %w", err)
	}
	return key, f.Sync()
}

// Digest implements Sink.
func (s *FileSink) Digest(args any) string { return digest(s.key, args) }

func digest(key []byte, args any) string {
	data, err := json.Marshal(args) // map keys marshal sorted: canonical enough
	if err != nil {
		data = []byte(fmt.Sprintf("%#v", args))
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

// Write implements Sink.
func (s *FileSink) Write(rec Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	unlock, lockErr := s.lock()
	if lockErr != nil {
		return Record{}, lockErr
	}
	defer unlock()

	now := s.now().UTC()
	file := filepath.Join(s.dir, now.Format("2006-01")+".jsonl")
	if err := s.refreshTail(); err != nil {
		return Record{}, err
	}

	var pending []Record
	switch {
	case s.file == "" && s.last == "":
		pending = append(pending, Record{Kind: KindChainStart, Note: "first record in this audit directory"})
	case s.file != file:
		pending = append(pending, Record{Kind: KindFileStart, PrevFile: filepath.Base(s.file), Note: "chain continues from the previous month's file"})
	}
	pending = append(pending, rec)

	var buf bytes.Buffer
	seq, last := s.seq, s.last
	var written Record
	for _, r := range pending {
		seq++
		r.Version = SchemaVersion
		r.Seq = seq
		if r.ID == "" {
			r.ID = NewID()
		}
		r.Time = now
		if r.Posture == "" {
			r.Posture = PostureSecure
		}
		r.PrevHash = last
		r.Hash = ""
		r.Hash = hashRecord(r)
		line, encErr := json.Marshal(r)
		if encErr != nil {
			return Record{}, fmt.Errorf("audit: encode record: %w", encErr)
		}
		buf.Write(line)
		buf.WriteByte('\n')
		last = r.Hash
		written = r
	}

	_, statErr := os.Stat(file)
	created := errors.Is(statErr, os.ErrNotExist)
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, fileMode) //nolint:gosec // the audit directory's own file
	if err != nil {
		return Record{}, fmt.Errorf("audit: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return Record{}, fmt.Errorf("audit: append %s: %w", file, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return Record{}, fmt.Errorf("audit: sync %s: %w", file, err)
	}
	info, infoErr := f.Stat()
	if err := f.Close(); err != nil {
		return Record{}, fmt.Errorf("audit: close %s: %w", file, err)
	}
	if created {
		syncDir(s.dir)
	}
	s.file, s.seq, s.last = file, seq, last
	if infoErr == nil {
		s.size = info.Size()
	}
	return written, nil
}

// lock takes the directory's cross-process lock.
func (s *FileSink) lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.dir, lockName), os.O_RDWR|os.O_CREATE, fileMode) //nolint:gosec // the audit directory's own lock file
	if err != nil {
		return nil, fmt.Errorf("audit: lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // fd fits in int
		_ = f.Close()
		return nil, fmt.Errorf("audit: lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // as above
		_ = f.Close()
	}, nil
}

// refreshTail re-reads the chain tail unless this sink wrote it last. It
// runs under the lock, so the tail it reads is the one this write follows.
func (s *FileSink) refreshTail() error {
	latest, err := latestFile(s.dir)
	if err != nil {
		return err
	}
	if latest == "" {
		s.file, s.size, s.seq, s.last = "", 0, 0, ""
		return nil
	}
	info, err := os.Stat(latest)
	if err != nil {
		return fmt.Errorf("audit: stat %s: %w", latest, err)
	}
	if latest == s.file && info.Size() == s.size {
		return nil
	}
	tail, torn, err := readTail(latest)
	if err != nil {
		return err
	}
	s.file, s.size = latest, info.Size()
	if tail != nil {
		s.seq, s.last = tail.Seq, tail.Hash
	}
	if torn {
		// A crash tore the last write. The chain resumes from the last
		// complete record, and says so; nothing is rewritten.
		if err := s.appendRaw(latest, Record{Kind: KindChainBreak, Note: "the previous write was torn; the chain resumes from the last complete record"}); err != nil {
			return err
		}
	}
	return nil
}

// appendRaw writes one record after a torn line, starting it on a new line.
func (s *FileSink) appendRaw(file string, rec Record) error {
	s.seq++
	rec.Version, rec.Seq, rec.ID, rec.Time, rec.Posture = SchemaVersion, s.seq, NewID(), s.now().UTC(), PostureSecure
	rec.PrevHash = s.last
	rec.Hash = hashRecord(rec)
	line, encErr := json.Marshal(rec)
	if encErr != nil {
		return encErr
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, fileMode) //nolint:gosec // as above
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	defer f.Close() //nolint:errcheck
	if _, err = f.Write(append(append([]byte{'\n'}, line...), '\n')); err != nil {
		return fmt.Errorf("audit: append %s: %w", file, err)
	}
	if err = f.Sync(); err != nil {
		return err
	}
	info, err := f.Stat()
	if err == nil {
		s.size = info.Size()
	}
	s.last = rec.Hash
	return nil
}

func latestFile(dir string) (string, error) {
	files, err := monthFiles(dir)
	if err != nil || len(files) == 0 {
		return "", err
	}
	return files[len(files)-1], nil
}

func monthFiles(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "[0-9][0-9][0-9][0-9]-[0-9][0-9].jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// readTail returns the last complete record of file, and whether the file
// ends in a torn (unterminated or unparseable) line.
func readTail(file string) (*Record, bool, error) {
	data, err := os.ReadFile(file) //nolint:gosec // the audit directory's own file
	if err != nil {
		return nil, false, fmt.Errorf("audit: read %s: %w", file, err)
	}
	torn := len(data) > 0 && data[len(data)-1] != '\n'
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		if len(bytes.TrimSpace(lines[i])) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(lines[i], &rec); err != nil {
			torn = true
			continue
		}
		return &rec, torn, nil
	}
	return nil, torn, nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil { //nolint:gosec // the audit directory
		_ = d.Sync()
		_ = d.Close()
	}
}

// hashRecord is the SHA-256 of the record's JSON with Hash empty. PrevHash
// is inside it, which is what chains the records.
func hashRecord(rec Record) string {
	rec.Hash = ""
	data, _ := json.Marshal(rec)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Verify walks every monthly file in dir in order and checks the chain:
// contiguous sequence numbers, each record's prev_hash the previous record's
// hash, each hash correct, and a file_start opening every file after the
// first. A torn line is a problem unless a chain_break follows it.
func Verify(dir string) error {
	files, err := monthFiles(dir)
	if err != nil {
		return err
	}
	var problems []string
	var seq uint64
	last := ""
	for i, file := range files {
		f, err := os.Open(file) //nolint:gosec // the audit directory's own file
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
		first, tornPending := true, false
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var rec Record
			if err := json.Unmarshal(line, &rec); err != nil {
				tornPending = true
				continue
			}
			name := filepath.Base(file)
			if first {
				switch {
				case i == 0 && rec.Kind != KindChainStart:
					problems = append(problems, name+": the first file does not open with chain_start")
				case i > 0 && rec.Kind != KindFileStart:
					problems = append(problems, name+": does not open with file_start")
				}
				first = false
			}
			if tornPending && rec.Kind != KindChainBreak {
				problems = append(problems, fmt.Sprintf("%s: a torn line before seq %d is not followed by chain_break", name, rec.Seq))
			}
			tornPending = false
			if rec.Seq != seq+1 {
				problems = append(problems, fmt.Sprintf("%s: seq %d follows %d", name, rec.Seq, seq))
			}
			if rec.PrevHash != last {
				problems = append(problems, fmt.Sprintf("%s: seq %d does not chain to the previous record", name, rec.Seq))
			}
			if hashRecord(rec) != rec.Hash {
				problems = append(problems, fmt.Sprintf("%s: seq %d hash does not match its content", name, rec.Seq))
			}
			seq, last = rec.Seq, rec.Hash
		}
		_ = f.Close()
		if err := scanner.Err(); err != nil {
			return err
		}
		if tornPending {
			problems = append(problems, filepath.Base(file)+": ends in a torn line")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("audit chain: %d problem(s): %s", len(problems), joinProblems(problems))
	}
	return nil
}

func joinProblems(p []string) string {
	var b bytes.Buffer
	for i, s := range p {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(s)
	}
	return b.String()
}
