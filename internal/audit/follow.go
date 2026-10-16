package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// A Cursor is a place in the audit chain: the next line to read, and the
// last record consumed before it. The zero Cursor is the start of the chain,
// which must open with chain_start.
type Cursor struct {
	// File is the base name of the month file holding the next line.
	File string `json:"file,omitempty"`
	// Offset is where that line starts.
	Offset int64 `json:"offset,omitempty"`
	// Seq and Hash are the last record consumed.
	Seq  uint64 `json:"seq,omitempty"`
	Hash string `json:"hash,omitempty"`
}

// StopReason says why a strict read stopped short of the end of the log.
type StopReason string

const (
	// StopDamaged: a line that does not parse and is not followed by the
	// chain_break record the sink writes to heal a torn write (the chain
	// verifies through that heal, so it is not a stop), or a torn line in a
	// file that is not the newest.
	StopDamaged StopReason = "damaged_line"
	// StopBroken: a record whose sequence, link or hash does not follow the
	// one before it, or a month file that does not open with file_start.
	StopBroken StopReason = "broken_chain"
	// StopPruned: the cursor's file is gone, as after a prune.
	StopPruned StopReason = "pruned"
	// StopCut: the log no longer holds the cursor's place or the anchored
	// head: it was cut or rewritten.
	StopCut StopReason = "cut"
)

// Stop is why a read ended before the log did. A reader that meets one must
// not skip it.
type Stop struct {
	Reason StopReason
	Detail string
}

func (s Stop) Error() string {
	return fmt.Sprintf("audit chain read stopped: %s: %s", s.Reason, s.Detail)
}

// Batch is what ReadFrom found. Records are complete, verified records after
// the cursor in order. Next is the cursor after the last of them (the one
// passed in when there were none). Stop is set when the read ended on a
// problem rather than at the end of the log; the next ReadFrom from Next
// meets the same problem until something changes.
type Batch struct {
	Records []Record
	Next    Cursor
	Stop    *Stop
}

// ErrUntrusted is returned by TailCursor when the chain does not vouch for its
// newest record.
var ErrUntrusted = errors.New("audit: the chain does not vouch for its newest record")

// ReadFrom returns up to limit (all when limit <= 0) complete, verified
// records after cur. Unlike ReadRecords it never skips: a line that does not
// parse (unless the sink healed it with a chain_break, which Check accepts and
// which is returned as a record), a record whose seq, prev_hash or hash does
// not follow, a month file that does not open with file_start, a missing file
// or a log shorter than the cursor ends the read with a Stop. A final line that has no newline yet
// is a write in progress and is simply not returned. ReadFrom takes no lock; a
// concurrent FileSink append is seen whole or not at all.
func ReadFrom(dir string, cur Cursor, limit int) (Batch, error) {
	out := Batch{Next: cur}
	if a := anchorFor(dir); a != nil {
		h, ok, err := a.Load()
		if err != nil {
			return out, fmt.Errorf("audit: the chain's anchor could not be read: %w", err)
		}
		if ok {
			if problem := headProblem(dir, h); problem != "" {
				out.Stop = &Stop{StopCut, problem}
				return out, nil
			}
		}
	}
	files, err := monthFiles(dir)
	if err != nil {
		return out, err
	}
	idx := 0
	if cur.File != "" {
		idx = -1
		for i, f := range files {
			if filepath.Base(f) == cur.File {
				idx = i
				break
			}
		}
		if idx < 0 {
			out.Stop = &Stop{StopPruned, cur.File + " is no longer in the audit directory"}
			return out, nil
		}
	}
	for ; idx < len(files); idx++ {
		stop, done, readErr := readFileFrom(files[idx], idx == len(files)-1, &out, limit)
		if readErr != nil {
			return out, readErr
		}
		if stop != nil {
			out.Stop = stop
			return out, nil //nolint:nilerr // a Stop is a result, not an error
		}
		if done {
			return out, nil
		}
	}
	return out, nil
}

// readFileFrom reads file from out.Next (or its start when the cursor is in
// an earlier file), appending to out. done is true when limit was reached or
// the file ended mid-line.
func readFileFrom(file string, newest bool, out *Batch, limit int) (stop *Stop, done bool, err error) {
	name := filepath.Base(file)
	offset := int64(0)
	if out.Next.File == name {
		offset = out.Next.Offset
	}
	f, err := os.Open(file) //nolint:gosec // the audit directory's own file
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if offset > info.Size() {
		return &Stop{StopCut, fmt.Sprintf("%s is shorter than the cursor (%d < %d): the log was cut", name, info.Size(), offset)}, false, nil
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return nil, false, err
	}
	// The first record of a file the cursor was not already inside must be
	// file_start (or chain_start for the very first file).
	opening := out.Next.File != name || offset == 0
	damagedAt := int64(-1) // start of a run of unparseable lines awaiting a chain_break
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, _, tooLong, readErr := ReadBoundedLine(r, MaxLineBytes)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, false, readErr
		}
		if tooLong {
			return &Stop{StopDamaged, fmt.Sprintf("%s: the line at offset %d is over %d bytes", name, offset, MaxLineBytes)}, false, nil
		}
		if len(line) == 0 {
			if damagedAt >= 0 {
				// Unparseable lines at the end with no chain_break yet.
				return &Stop{StopDamaged, fmt.Sprintf("%s: the line at offset %d does not parse", name, damagedAt)}, false, nil
			}
			return nil, false, nil // end of file
		}
		if line[len(line)-1] != '\n' {
			if newest && damagedAt < 0 {
				return nil, true, nil // a write in progress
			}
			return &Stop{StopDamaged, name + " ends in a torn line"}, false, nil
		}
		next := offset + int64(len(line))
		body := bytes.TrimSpace(line)
		if len(body) == 0 {
			offset = next
			continue
		}
		var rec Record
		if json.Unmarshal(body, &rec) != nil {
			// A torn write. The sink heals it by starting the next record on a
			// new line as a chain_break; that is the only thing that may follow.
			if damagedAt < 0 {
				damagedAt = offset
			}
			offset = next
			continue
		}
		if damagedAt >= 0 && rec.Kind != KindChainBreak {
			return &Stop{StopDamaged, fmt.Sprintf("%s: the line at offset %d does not parse and no chain_break follows it", name, damagedAt)}, false, nil
		}
		if problem := chainProblem(rec, body, out.Next, opening); problem != "" {
			return &Stop{StopBroken, fmt.Sprintf("%s: %s", name, problem)}, false, nil
		}
		opening, damagedAt = false, -1
		out.Records = append(out.Records, rec)
		out.Next = Cursor{File: name, Offset: next, Seq: rec.Seq, Hash: rec.Hash}
		offset = next
		if limit > 0 && len(out.Records) >= limit {
			return nil, true, nil
		}
	}
}

// chainProblem is what is wrong with rec following the record cursor
// describes, or "".
func chainProblem(rec Record, raw []byte, after Cursor, opening bool) string {
	switch {
	case opening && after.Seq == 0 && rec.Kind != KindChainStart:
		return fmt.Sprintf("the chain does not open with chain_start (seq %d is %s)", rec.Seq, rec.Kind)
	case opening && after.Seq != 0 && rec.Kind != KindFileStart:
		return fmt.Sprintf("a new month file does not open with file_start (seq %d is %s)", rec.Seq, rec.Kind)
	case rec.Seq != after.Seq+1:
		return fmt.Sprintf("seq %d follows %d", rec.Seq, after.Seq)
	case rec.PrevHash != after.Hash:
		return fmt.Sprintf("seq %d does not chain to the previous record", rec.Seq)
	case !LineHashMatches(raw, rec.Hash) && hashRecord(rec) != rec.Hash:
		return fmt.Sprintf("seq %d hash does not match its content", rec.Seq)
	}
	return ""
}

// TailCursor returns a cursor at the end of the log's newest complete record,
// for a reader that starts "from now" with no backfill. It refuses
// (ErrUntrusted) when the chain does not vouch for that record. An empty
// directory yields the zero Cursor.
func TailCursor(dir string) (Cursor, error) {
	checked, err := Check(dir)
	if err != nil {
		return Cursor{}, err
	}
	if len(checked.Records) == 0 {
		return Cursor{}, nil
	}
	if !checked.TailTrusted() {
		return Cursor{}, ErrUntrusted
	}
	last := checked.Records[len(checked.Records)-1]
	files, err := monthFiles(dir)
	if err != nil || len(files) == 0 {
		return Cursor{}, errors.Join(err, errors.New("audit: no month files"))
	}
	pos, err := positionAfter(files[len(files)-1], last.Seq)
	if err != nil {
		return Cursor{}, err
	}
	return Cursor{File: filepath.Base(files[len(files)-1]), Offset: pos, Seq: last.Seq, Hash: last.Hash}, nil
}

// ResumeAfterReanchor finds the first chain_reanchored record after cur whose
// record the chain vouches for, and returns the cursor just past it. ok is
// false when there is none yet: the reader stays stopped.
func ResumeAfterReanchor(dir string, cur Cursor) (next Cursor, ok bool, err error) {
	checked, err := Check(dir)
	if err != nil {
		return Cursor{}, false, err
	}
	for i, rec := range checked.Records {
		if rec.Seq <= cur.Seq || rec.Kind != KindChainReanchored || !checked.Trusted[i] || checked.cut {
			continue
		}
		file, pos, err := locate(dir, rec.Seq)
		if err != nil {
			return Cursor{}, false, err
		}
		return Cursor{File: file, Offset: pos, Seq: rec.Seq, Hash: rec.Hash}, true, nil
	}
	return Cursor{}, false, nil
}

// locate returns the file holding record seq and the offset just past its line.
func locate(dir string, seq uint64) (string, int64, error) {
	files, err := monthFiles(dir)
	if err != nil {
		return "", 0, err
	}
	for _, file := range files {
		if pos, perr := positionAfter(file, seq); perr == nil && pos > 0 {
			return filepath.Base(file), pos, nil
		}
	}
	return "", 0, fmt.Errorf("audit: seq %d is not in any month file", seq)
}

// ReadBoundedLine reads one line (including its newline, when it has one) of at
// most limit bytes. n is the number of bytes consumed, which is the line's length
// even when it is too long to keep: a longer line is consumed to its end without
// being retained and reported as tooLong, so a corrupted file with no newline
// cannot make a reader allocate without bound.
func ReadBoundedLine(r *bufio.Reader, limit int) (line []byte, n int, tooLong bool, err error) {
	for {
		chunk, readErr := r.ReadSlice('\n')
		n += len(chunk)
		if !tooLong {
			if len(line)+len(chunk) > limit+1 {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		return line, n, tooLong, readErr
	}
}

// positionAfter is the offset just past the line of record seq in file, or 0
// when the file does not hold it.
func positionAfter(file string, seq uint64) (int64, error) {
	f, err := os.Open(file) //nolint:gosec // the audit directory's own file
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 64*1024)
	var offset int64
	for {
		line, n, tooLong, readErr := ReadBoundedLine(r, MaxLineBytes)
		if tooLong {
			return 0, nil // a damaged line: the cursor cannot be placed past it
		}
		offset += int64(n)
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var rec Record
			if json.Unmarshal(bytes.TrimSpace(line), &rec) == nil && rec.Seq == seq {
				return offset, nil
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return 0, nil
			}
			return 0, readErr
		}
	}
}

// CursorValid reports whether cur still names a place in the log: its file
// exists, holds at least cur.Offset bytes, and the record that ends there is
// cur.Seq with cur.Hash. A log restored to an older copy, or rewritten, fails
// it. The zero Cursor is always valid.
func CursorValid(dir string, cur Cursor) (bool, error) {
	if cur == (Cursor{}) {
		return true, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.Base(cur.File))) //nolint:gosec // the audit directory's own file
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return endsWithRecord(data, cur.Offset, cur.Seq, cur.Hash), nil
}

// endsWithRecord reports whether the line that ends at offset in data is the
// record seq with hash.
func endsWithRecord(data []byte, offset int64, seq uint64, hash string) bool {
	if offset <= 0 || offset > int64(len(data)) || data[offset-1] != '\n' {
		return false
	}
	prefix := bytes.TrimRight(data[:offset], "\n")
	start := bytes.LastIndexByte(prefix, '\n') + 1
	var rec Record
	return json.Unmarshal(prefix[start:], &rec) == nil && rec.Seq == seq && rec.Hash == hash
}
