package approval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hollis-labs/cerberus/internal/audit"
)

// A Cursor is a place in the approval event store: the next line to read and
// the last event consumed before it. The zero Cursor is the start of the file.
type Cursor struct {
	Offset int64  `json:"offset,omitempty"`
	Seq    uint64 `json:"seq,omitempty"`
	Hash   string `json:"hash,omitempty"`
}

// StopReason says why a strict read stopped short of the end of the store.
type StopReason string

const (
	// StopBroken: an event whose sequence, link or hash does not follow the
	// one before it, or a line over the size a record may be. The store folds
	// past it as restrictive events only; a reader does not read past it until
	// the daemon has taken the store up again with a reanchored event.
	StopBroken StopReason = "broken_chain"
	// StopCut: the file is shorter than the cursor, or gone.
	StopCut StopReason = "cut"
)

// Stop is why a read ended before the store did.
type Stop struct {
	Reason StopReason
	Detail string
}

func (s Stop) Error() string {
	return fmt.Sprintf("approval store read stopped: %s: %s", s.Reason, s.Detail)
}

// Batch is what ReadFrom found. Events are complete, chained events after the
// cursor in order, and Next the cursor after the last of them. Damaged holds
// the offsets of lines that did not parse and were passed over because the
// event after them chains from the last good one, as the store's own fold does;
// a reader should report them. Stop is set when the read ended on a problem.
type Batch struct {
	Events  []Event
	Next    Cursor
	Damaged []int64
	Stop    *Stop
}

// ErrUntrusted is returned by TailCursor when the store's chain is not intact.
var ErrUntrusted = errors.New("approvals: the store's chain is not intact")

// ReadFrom returns up to limit (all when limit <= 0) complete, chained events
// after cur. A final line without a newline is a write in progress and is not
// returned. The store's one writer is the daemon, so ReadFrom takes no lock; an
// append is seen whole or not at all.
func ReadFrom(dir string, cur Cursor, limit int) (Batch, error) {
	out := Batch{Next: cur}
	f, err := os.Open(filepath.Join(dir, FileName)) //nolint:gosec // the store's own file
	if errors.Is(err, os.ErrNotExist) {
		if cur == (Cursor{}) {
			return out, nil
		}
		out.Stop = &Stop{StopCut, FileName + " is gone"}
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return out, err
	}
	if cur.Offset > info.Size() {
		out.Stop = &Stop{StopCut, fmt.Sprintf("%s is shorter than the cursor (%d < %d)", FileName, info.Size(), cur.Offset)}
		return out, nil
	}
	if _, err = f.Seek(cur.Offset, io.SeekStart); err != nil {
		return out, err
	}
	r := bufio.NewReaderSize(f, 64*1024)
	offset := cur.Offset
	var pendingDamage []int64
	for {
		line, readErr := r.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return out, readErr
		}
		if len(line) == 0 || line[len(line)-1] != '\n' {
			return out, nil // end of file, or a write in progress
		}
		next := offset + int64(len(line))
		body := bytes.TrimSpace(line)
		if len(body) == 0 {
			offset = next
			continue
		}
		if len(line) > audit.MaxLineBytes {
			out.Stop = &Stop{StopBroken, fmt.Sprintf("the line at offset %d is over %d bytes", offset, audit.MaxLineBytes)}
			return out, nil
		}
		var ev Event
		if json.Unmarshal(body, &ev) != nil {
			pendingDamage = append(pendingDamage, offset)
			offset = next
			continue
		}
		if !chained(ev, body, out.Next) {
			out.Stop = &Stop{StopBroken, fmt.Sprintf("seq %d does not chain to the event before it (seq %d)", ev.Seq, out.Next.Seq)}
			return out, nil
		}
		out.Damaged = append(out.Damaged, pendingDamage...)
		pendingDamage = nil
		out.Events = append(out.Events, ev)
		out.Next = Cursor{Offset: next, Seq: ev.Seq, Hash: ev.Hash}
		offset = next
		if limit > 0 && len(out.Events) >= limit {
			return out, nil
		}
	}
}

func chained(ev Event, raw []byte, after Cursor) bool {
	return ev.Seq == after.Seq+1 && ev.PrevHash == after.Hash && (audit.LineHashMatches(raw, ev.Hash) || hashEvent(ev) == ev.Hash)
}

// TailCursor returns a cursor at the end of the store's newest complete
// event, for a reader that starts "from now" with no backfill. It refuses
// (ErrUntrusted) when the store's chain is broken. A missing or empty store
// yields the zero Cursor.
func TailCursor(dir string) (Cursor, error) {
	batch, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil {
		return Cursor{}, err
	}
	if batch.Stop != nil {
		return Cursor{}, fmt.Errorf("%w: %w", ErrUntrusted, batch.Stop)
	}
	return batch.Next, nil
}

// ResumeAfterReanchor finds the first reanchored event after cur and returns
// the cursor just past it, tracking the chain the way the store's fold does
// (it follows the file past a break). ok is false when there is none yet: the
// daemon has not taken the store up again.
func ResumeAfterReanchor(dir string, cur Cursor) (next Cursor, ok bool, err error) {
	f, err := os.Open(filepath.Join(dir, FileName)) //nolint:gosec // the store's own file
	if errors.Is(err, os.ErrNotExist) {
		return Cursor{}, false, nil
	}
	if err != nil {
		return Cursor{}, false, err
	}
	defer func() { _ = f.Close() }()
	if _, err = f.Seek(cur.Offset, io.SeekStart); err != nil {
		return Cursor{}, false, err
	}
	r := bufio.NewReaderSize(f, 64*1024)
	offset, seq, last := cur.Offset, cur.Seq, cur.Hash
	for {
		line, readErr := r.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return Cursor{}, false, readErr
		}
		if len(line) == 0 || line[len(line)-1] != '\n' {
			return Cursor{}, false, nil
		}
		offset += int64(len(line))
		var ev Event
		body := bytes.TrimSpace(line)
		if len(body) == 0 || json.Unmarshal(body, &ev) != nil {
			continue
		}
		if ev.Type == EventReanchored && chained(ev, body, Cursor{Seq: seq, Hash: last}) {
			return Cursor{Offset: offset, Seq: ev.Seq, Hash: ev.Hash}, true, nil
		}
		seq, last = ev.Seq, ev.Hash
	}
}

// CursorValid reports whether cur still names a place in the store: the file
// holds at least cur.Offset bytes and the event that ends there is cur.Seq with
// cur.Hash. A store restored to an older copy, or rewritten, fails it. The zero
// Cursor is always valid.
func CursorValid(dir string, cur Cursor) (bool, error) {
	if cur == (Cursor{}) {
		return true, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName)) //nolint:gosec // the store's own file
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if cur.Offset <= 0 || cur.Offset > int64(len(data)) || data[cur.Offset-1] != '\n' {
		return false, nil
	}
	prefix := bytes.TrimRight(data[:cur.Offset], "\n")
	start := bytes.LastIndexByte(prefix, '\n') + 1
	var ev Event
	return json.Unmarshal(prefix[start:], &ev) == nil && ev.Seq == cur.Seq && ev.Hash == cur.Hash, nil
}
