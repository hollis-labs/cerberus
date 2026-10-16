package audit

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// movingClock lets a test step across a month boundary.
type movingClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *movingClock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *movingClock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func followSink(t *testing.T, start time.Time) (*FileSink, string, *movingClock) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "audit")
	clk := &movingClock{t: start}
	sink, err := openFileSink(dir, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	return sink, dir, clk
}

func writeOps(t *testing.T, sink *FileSink, from, count int) {
	t.Helper()
	for i := range count {
		if _, err := sink.Write(Record{Kind: KindIntent, Operation: fmt.Sprintf("op-%d", from+i)}); err != nil {
			t.Fatal(err)
		}
	}
}

func seqs(records []Record) []uint64 {
	out := make([]uint64, len(records))
	for i, r := range records {
		out[i] = r.Seq
	}
	return out
}

func dirDigest(t *testing.T, dir string) [32]byte {
	t.Helper()
	h := sha256.New()
	files, _ := monthFiles(dir)
	for _, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // the test's own file
		if err != nil {
			t.Fatal(err)
		}
		h.Write(data)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func TestReadFromReturnsEveryRecordOnceAndLeavesTheChainAlone(t *testing.T) {
	sink, dir, _ := followSink(t, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 0, 6) // plus chain_start: 7 records
	before := dirDigest(t, dir)

	var got []Record
	cur := Cursor{}
	for range 10 {
		batch, err := ReadFrom(dir, cur, 3)
		if err != nil || batch.Stop != nil {
			t.Fatalf("batch = %+v, %v", batch, err)
		}
		if len(batch.Records) == 0 {
			break
		}
		got = append(got, batch.Records...)
		cur = batch.Next
	}
	all, err := ReadRecords(dir)
	if err != nil || len(got) != len(all) || len(got) != 7 {
		t.Fatalf("read %d of %d records (%v)", len(got), len(all), err)
	}
	for i, rec := range got {
		if rec.Seq != uint64(i+1) || rec.Hash != all[i].Hash {
			t.Fatalf("record %d = seq %d", i, rec.Seq)
		}
	}
	// New records continue from the cursor without repeats.
	writeOps(t, sink, 6, 2)
	batch, err := ReadFrom(dir, cur, 0)
	if err != nil || batch.Stop != nil || fmt.Sprint(seqs(batch.Records)) != "[8 9]" {
		t.Fatalf("after more writes: %+v, %v", batch, err)
	}
	if after := dirDigest(t, dir); after == before && len(batch.Records) > 0 {
		t.Fatal("test bug: the digest should have changed after writes")
	}
	if err := Verify(dir); err != nil {
		t.Fatalf("reading broke the chain: %v", err)
	}
}

func TestReadFromCrossesMonthFiles(t *testing.T) {
	sink, dir, clk := followSink(t, time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 0, 2)
	clk.set(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 2, 2)
	batch, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil || batch.Stop != nil {
		t.Fatalf("batch = %+v, %v", batch, err)
	}
	want := []string{KindChainStart, KindIntent, KindIntent, KindFileStart, KindIntent, KindIntent}
	if len(batch.Records) != len(want) || batch.Next.File != "2026-10.jsonl" {
		t.Fatalf("records=%d next=%+v", len(batch.Records), batch.Next)
	}
	for i, rec := range batch.Records {
		if rec.Kind != want[i] || rec.Seq != uint64(i+1) {
			t.Fatalf("record %d = %s seq %d", i, rec.Kind, rec.Seq)
		}
	}
}

func TestTailCursorStartsFromNowWithNoBackfill(t *testing.T) {
	empty, err := TailCursor(t.TempDir())
	if err != nil || empty != (Cursor{}) {
		t.Fatalf("empty dir tail = %+v, %v", empty, err)
	}
	sink, dir, _ := followSink(t, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 0, 4)
	tail, err := TailCursor(dir)
	if err != nil || tail.Seq != 5 {
		t.Fatalf("tail = %+v, %v", tail, err)
	}
	writeOps(t, sink, 4, 2)
	batch, err := ReadFrom(dir, tail, 0)
	if err != nil || batch.Stop != nil || fmt.Sprint(seqs(batch.Records)) != "[6 7]" {
		t.Fatalf("after tail: %+v, %v", batch, err)
	}

	editLog(t, dir, func(data []byte) []byte {
		return replaceOnce(data, `"operation":"op-1"`, `"operation":"OP-1"`)
	})
	if _, err := TailCursor(dir); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("tampered chain: %v", err)
	}
}

func TestReadFromWaitsOutAWriteInProgress(t *testing.T) {
	sink, dir, _ := followSink(t, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 0, 2) // seqs 1..3
	files, _ := monthFiles(dir)
	data, err := os.ReadFile(files[0]) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	// The last line's newline has not been written yet.
	if writeErr := os.WriteFile(files[0], data[:len(data)-1], fileMode); writeErr != nil { //nolint:gosec // the test's own file
		t.Fatal(writeErr)
	}
	batch, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil || batch.Stop != nil || fmt.Sprint(seqs(batch.Records)) != "[1 2]" {
		t.Fatalf("partial line: %+v, %v", batch, err)
	}
	if writeErr := os.WriteFile(files[0], data, fileMode); writeErr != nil { //nolint:gosec // the test's own file
		t.Fatal(writeErr)
	}
	more, err := ReadFrom(dir, batch.Next, 0)
	if err != nil || more.Stop != nil || fmt.Sprint(seqs(more.Records)) != "[3]" {
		t.Fatalf("completed line: %+v, %v", more, err)
	}
}

func TestReadFromStopsOnATamperedRecordAndNeverSkipsIt(t *testing.T) {
	sink, dir, _ := followSink(t, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 0, 4)
	editLog(t, dir, func(data []byte) []byte {
		return replaceOnce(data, `"operation":"op-2"`, `"operation":"OP-2"`)
	})
	batch, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil || batch.Stop == nil || batch.Stop.Reason != StopBroken {
		t.Fatalf("tampered: %+v, %v", batch, err)
	}
	if fmt.Sprint(seqs(batch.Records)) != "[1 2 3]" {
		t.Fatalf("records before the edit = %v", seqs(batch.Records))
	}
	again, _ := ReadFrom(dir, batch.Next, 0)
	if again.Stop == nil || len(again.Records) != 0 {
		t.Fatalf("a reader must stay stopped at the bad record: %+v", again)
	}
}

// The sink heals a torn write with a chain_break that Check accepts, and there
// is then nothing to reanchor. A strict reader must read through that heal
// (delivering the chain_break) rather than stop for ever.
func TestReadFromReadsThroughAHealedTornWrite(t *testing.T) {
	sink, dir, _ := followSink(t, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 0, 2) // 1..3
	files, _ := monthFiles(dir)
	f, err := os.OpenFile(files[0], os.O_WRONLY|os.O_APPEND, fileMode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"v":1,"seq":4,"id":"torn`); err != nil { // a crash mid-write
		t.Fatal(err)
	}
	_ = f.Close()

	// Until the sink heals it, a torn tail on the newest file cannot be told
	// from a write in progress: it is not returned, and not yet a stop.
	pending, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil || pending.Stop != nil || fmt.Sprint(seqs(pending.Records)) != "[1 2 3]" {
		t.Fatalf("unhealed torn tail: %+v, %v", pending, err)
	}

	writeOps(t, sink, 2, 1) // the sink heals: chain_break (seq 4), then the record (seq 5)
	if verifyErr := Verify(dir); verifyErr != nil {
		t.Fatalf("test assumption: the healed chain verifies: %v", verifyErr)
	}
	batch, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil || batch.Stop != nil || fmt.Sprint(seqs(batch.Records)) != "[1 2 3 4 5]" {
		t.Fatalf("healed torn write: %+v, %v", batch, err)
	}
	if batch.Records[3].Kind != KindChainBreak {
		t.Fatalf("the chain_break must be delivered, got %s", batch.Records[3].Kind)
	}
}

func TestReadFromStopsAtADamagedLineNothingHealed(t *testing.T) {
	sink, dir, _ := followSink(t, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 0, 4) // 1..5
	editLog(t, dir, func(data []byte) []byte {
		lines := bytes.Split(data, []byte("\n"))
		lines[3] = []byte("garbage") // seq 4
		return bytes.Join(lines, []byte("\n"))
	})
	batch, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil || batch.Stop == nil || batch.Stop.Reason != StopDamaged || fmt.Sprint(seqs(batch.Records)) != "[1 2 3]" {
		t.Fatalf("damaged line: %+v, %v", batch, err)
	}
	if again, _ := ReadFrom(dir, batch.Next, 0); again.Stop == nil || len(again.Records) != 0 {
		t.Fatalf("a reader must stay stopped: %+v", again)
	}
}

func TestReadFromResumesAfterAReanchorOnly(t *testing.T) {
	sink, dir, _ := followSink(t, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 0, 4) // 1..5
	editLog(t, dir, func(data []byte) []byte {
		return replaceOnce(data, `"operation":"op-1"`, `"operation":"OP-1"`)
	})
	batch, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil || batch.Stop == nil || batch.Stop.Reason != StopBroken || fmt.Sprint(seqs(batch.Records)) != "[1 2]" {
		t.Fatalf("tampered: %+v, %v", batch, err)
	}
	if _, ok, resumeErr := ResumeAfterReanchor(dir, batch.Next); resumeErr != nil || ok {
		t.Fatalf("resume before a reanchor: ok=%v err=%v", ok, resumeErr)
	}
	if _, reanchorErr := Reanchor(sink, dir, Principal{Surface: "test"}); reanchorErr != nil {
		t.Fatal(reanchorErr)
	}
	next, ok, err := ResumeAfterReanchor(dir, batch.Next)
	if err != nil || !ok || next.Seq != 6 {
		t.Fatalf("resume after reanchor: %+v ok=%v err=%v", next, ok, err)
	}
	writeOps(t, sink, 4, 1)
	rest, err := ReadFrom(dir, next, 0)
	if err != nil || rest.Stop != nil || len(rest.Records) != 1 || rest.Records[0].Operation != "op-4" {
		t.Fatalf("after resume: %+v, %v", rest, err)
	}
}

func TestReadFromDeliversAnExplicitChainBreakRecordAndKeepsGoing(t *testing.T) {
	sink, dir, _ := followSink(t, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 0, 1)
	if _, err := sink.Write(Record{Kind: KindChainBreak, Note: "test break"}); err != nil {
		t.Fatal(err)
	}
	writeOps(t, sink, 1, 1)
	batch, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil || batch.Stop != nil || len(batch.Records) != 4 || batch.Records[2].Kind != KindChainBreak {
		t.Fatalf("batch = %+v, %v", batch, err)
	}
}

func TestReadFromReportsAPrunedOrCutLog(t *testing.T) {
	sink, dir, clk := followSink(t, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 0, 2)
	cur, err := TailCursor(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := monthFiles(dir)
	clk.set(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 2, 1)

	// Cut: the file is shorter than the cursor.
	data, _ := os.ReadFile(files[0])
	if err := os.WriteFile(files[0], data[:cur.Offset-10], fileMode); err != nil { //nolint:gosec // the test's own file
		t.Fatal(err)
	}
	if batch, _ := ReadFrom(dir, cur, 0); batch.Stop == nil || batch.Stop.Reason != StopCut {
		t.Fatalf("cut log: %+v", batch)
	}
	// Pruned: the cursor's file is gone.
	if err := os.Remove(files[0]); err != nil { //nolint:gosec // the test's own file
		t.Fatal(err)
	}
	if batch, _ := ReadFrom(dir, cur, 0); batch.Stop == nil || batch.Stop.Reason != StopPruned {
		t.Fatalf("pruned log: %+v", batch)
	}
}

// A reader following a log while a writer appends must see every record
// once, in order, and never a partial one.
func TestReadFromFollowsAConcurrentWriter(t *testing.T) {
	sink, dir, _ := followSink(t, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	const total = 300
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range total {
			if _, err := sink.Write(Record{Kind: KindIntent, Operation: fmt.Sprintf("op-%d", i)}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	var got []uint64
	cur := Cursor{}
	writerDone := false
	for range 100000 {
		batch, err := ReadFrom(dir, cur, 17)
		if err != nil || batch.Stop != nil {
			t.Fatalf("batch = %+v, %v", batch, err)
		}
		got = append(got, seqs(batch.Records)...)
		cur = batch.Next
		if writerDone && len(batch.Records) == 0 {
			break
		}
		select {
		case <-done:
			writerDone = true
		default:
		}
	}
	if len(got) != total+1 {
		t.Fatalf("read %d records, want %d", len(got), total+1)
	}
	for i, seq := range got {
		if seq != uint64(i+1) {
			t.Fatalf("lost or repeated a record at %d: %v", i, got[max(0, i-3):i+1])
		}
	}
}

func replaceOnce(data []byte, old, replacement string) []byte {
	return bytes.Replace(data, []byte(old), []byte(replacement), 1)
}
