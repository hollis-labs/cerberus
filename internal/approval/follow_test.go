package approval

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func eventSeqs(events []Event) []uint64 {
	out := make([]uint64, len(events))
	for i, e := range events {
		out[i] = e.Seq
	}
	return out
}

func TestReadFromReturnsEveryEventOnceAcrossCalls(t *testing.T) {
	s, _, dir := newStore(t)
	a := request(t, s, ChannelTTYConfirm)
	if _, err := s.Decide(a.ID, Decision{Approve: true, By: human}, nil); err != nil {
		t.Fatal(err)
	}
	request(t, s, ChannelTTYConfirm)

	var got []Event
	cur := Cursor{}
	for range 10 {
		batch, err := ReadFrom(dir, cur, 2)
		if err != nil || batch.Stop != nil || len(batch.Damaged) != 0 {
			t.Fatalf("batch = %+v, %v", batch, err)
		}
		if len(batch.Events) == 0 {
			break
		}
		got = append(got, batch.Events...)
		cur = batch.Next
	}
	if fmt.Sprint(eventSeqs(got)) != "[1 2 3]" || got[0].Type != EventRequested || got[1].Type != EventDecided {
		t.Fatalf("events = %v", eventSeqs(got))
	}
	request(t, s, ChannelTTYConfirm)
	more, err := ReadFrom(dir, cur, 0)
	if err != nil || more.Stop != nil || fmt.Sprint(eventSeqs(more.Events)) != "[4]" {
		t.Fatalf("after another request: %+v, %v", more, err)
	}
}

func TestTailCursorStartsFromNowAndRefusesABrokenStore(t *testing.T) {
	if cur, err := TailCursor(t.TempDir()); err != nil || cur != (Cursor{}) {
		t.Fatalf("missing store tail = %+v, %v", cur, err)
	}
	s, _, dir := newStore(t)
	request(t, s, ChannelTTYConfirm)
	request(t, s, ChannelTTYConfirm)
	tail, err := TailCursor(dir)
	if err != nil || tail.Seq != 2 {
		t.Fatalf("tail = %+v, %v", tail, err)
	}
	request(t, s, ChannelTTYConfirm)
	batch, err := ReadFrom(dir, tail, 0)
	if err != nil || fmt.Sprint(eventSeqs(batch.Events)) != "[3]" {
		t.Fatalf("after tail: %+v, %v", batch, err)
	}
	appendRaw(t, dir, Event{V: eventVersion, Seq: 99, Type: EventExpired, ApprovalID: "apr_x", PrevHash: "x", Hash: "y"})
	if _, err := TailCursor(dir); err == nil {
		t.Fatal("a broken store gave a tail cursor")
	}
}

func TestReadFromWaitsOutAWriteInProgress(t *testing.T) {
	s, _, dir := newStore(t)
	request(t, s, ChannelTTYConfirm)
	request(t, s, ChannelTTYConfirm)
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(path, data[:len(data)-1], 0o600); writeErr != nil { //nolint:gosec // the test's own file
		t.Fatal(writeErr)
	}
	batch, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil || batch.Stop != nil || fmt.Sprint(eventSeqs(batch.Events)) != "[1]" {
		t.Fatalf("partial line: %+v, %v", batch, err)
	}
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil { //nolint:gosec // the test's own file
		t.Fatal(writeErr)
	}
	more, err := ReadFrom(dir, batch.Next, 0)
	if err != nil || fmt.Sprint(eventSeqs(more.Events)) != "[2]" {
		t.Fatalf("completed line: %+v, %v", more, err)
	}
}

// The store's own fold skips a line that does not parse when the event after it
// chains from the last good one; the reader does the same but reports it.
func TestReadFromReportsADamagedLineItPassesOver(t *testing.T) {
	s, _, dir := newStore(t)
	request(t, s, ChannelTTYConfirm)
	path := filepath.Join(dir, FileName)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the test's own store
	_, _ = f.WriteString("{not json\n")
	_ = f.Close()
	request(t, s, ChannelTTYConfirm) // chains from seq 1, as the daemon would
	batch, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil || batch.Stop != nil || fmt.Sprint(eventSeqs(batch.Events)) != "[1 2]" || len(batch.Damaged) != 1 {
		t.Fatalf("batch = %+v, %v", batch, err)
	}
}

func TestReadFromStopsAtABrokenLinkAndResumesAfterTheDaemonReanchors(t *testing.T) {
	s, c, dir := newStore(t)
	a := request(t, s, ChannelTTYConfirm)
	request(t, s, ChannelTTYConfirm)
	appendRaw(t, dir, Event{V: eventVersion, Seq: 99, Type: EventConsumed, ApprovalID: a.ID, PrevHash: "x", Hash: "y"})

	batch, err := ReadFrom(dir, Cursor{}, 0)
	if err != nil || batch.Stop == nil || batch.Stop.Reason != StopBroken || fmt.Sprint(eventSeqs(batch.Events)) != "[1 2]" {
		t.Fatalf("broken link: %+v, %v", batch, err)
	}
	if _, ok, resumeErr := ResumeAfterReanchor(dir, batch.Next); resumeErr != nil || ok {
		t.Fatalf("resume before the daemon reanchors: ok=%v err=%v", ok, resumeErr)
	}
	// Opening the store is the daemon taking it up again: it appends reanchored.
	reopened, err := open(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	next, ok, err := ResumeAfterReanchor(dir, batch.Next)
	if err != nil || !ok {
		t.Fatalf("resume after reanchor: ok=%v err=%v", ok, err)
	}
	request(t, reopened, ChannelTTYConfirm)
	rest, err := ReadFrom(dir, next, 0)
	if err != nil || rest.Stop != nil || len(rest.Events) != 1 || rest.Events[0].Type != EventRequested {
		t.Fatalf("after resume: %+v, %v", rest, err)
	}
}

func TestReadFromReportsACutStore(t *testing.T) {
	s, _, dir := newStore(t)
	request(t, s, ChannelTTYConfirm)
	request(t, s, ChannelTTYConfirm)
	tail, err := TailCursor(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	if err := os.Truncate(path, tail.Offset/2); err != nil { //nolint:gosec // the test's own file
		t.Fatal(err)
	}
	if batch, _ := ReadFrom(dir, tail, 0); batch.Stop == nil || batch.Stop.Reason != StopCut {
		t.Fatalf("cut store: %+v", batch)
	}
	if err := os.Remove(path); err != nil { //nolint:gosec // the test's own file
		t.Fatal(err)
	}
	if batch, _ := ReadFrom(dir, tail, 0); batch.Stop == nil || batch.Stop.Reason != StopCut {
		t.Fatalf("missing store: %+v", batch)
	}
}

func TestReadFromFollowsTheDaemonWriting(t *testing.T) {
	s, _, dir := newStore(t)
	const total = 200
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range total {
			if _, err := s.Request(Approval{Principal: asker, Connector: "kubernetes", Operation: "delete_pod", Effect: "destructive", Channel: ChannelTTYConfirm}, time.Hour); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	var got []uint64
	cur := Cursor{}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	writerDone := false
	for range 100000 {
		batch, err := ReadFrom(dir, cur, 13)
		if err != nil || batch.Stop != nil {
			t.Fatalf("batch = %+v, %v", batch, err)
		}
		got = append(got, eventSeqs(batch.Events)...)
		cur = batch.Next
		if writerDone && len(batch.Events) == 0 {
			break
		}
		select {
		case <-finished:
			writerDone = true
		default:
		}
	}
	if len(got) != total {
		t.Fatalf("read %d events, want %d", len(got), total)
	}
	for i, seq := range got {
		if seq != uint64(i+1) {
			t.Fatalf("lost or repeated an event at %d", i)
		}
	}
}
