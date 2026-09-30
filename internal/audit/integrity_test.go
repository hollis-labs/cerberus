package audit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A string that is not valid UTF-8 is written as the escape � and
// reads back as U+FFFD, which re-marshals differently. The record is
// verified from the bytes it was hashed as, not from a re-marshal (M2): a
// caller's name clipped mid-character no longer breaks the chain for good.
// Records written before this check exist on disk in exactly this form.
func TestVerifyHashesTheBytesWritten(t *testing.T) {
	dir := t.TempDir()
	sink, err := openFileSink(dir, clock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	clipped := strings.Repeat("é", 63) + "\xc3" // 127 bytes, the last one half a character
	for _, rec := range []Record{
		{Kind: KindIntent, Principal: Principal{Surface: "socket", Client: clipped}},
		{Kind: KindOutcome, PluginTelemetry: &PluginTelemetry{Stderr: []string{"\xff\xfe"}}},
	} {
		if _, err := sink.Write(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := Verify(dir); err != nil {
		t.Fatalf("a record with invalid UTF-8 does not verify: %v", err)
	}
	// And a real edit to such a record is still caught.
	editLog(t, dir, func(data []byte) []byte {
		return bytes.Replace(data, []byte(`"surface":"socket"`), []byte(`"surface":"sockeT"`), 1)
	})
	if err := Verify(dir); err == nil {
		t.Fatal("an edit to the record was not caught")
	}
}

// A record past a problem is not vouched for until a person reanchors the
// chain; the reanchor does not make Verify pass (M1).
func TestCheckTrustsOnlyUpToABreakAndFromAReanchor(t *testing.T) {
	dir := t.TempDir()
	sink, err := openFileSink(dir, clock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"one", "two", "three"} {
		if _, werr := sink.Write(Record{Kind: KindIntent, Operation: op}); werr != nil {
			t.Fatal(werr)
		}
	}
	editLog(t, dir, func(data []byte) []byte {
		return bytes.Replace(data, []byte(`"operation":"two"`), []byte(`"operation":"TWO"`), 1)
	})
	c, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	// chain_start, one, TWO, three
	if want := []bool{true, true, false, false}; !equalBools(c.Trusted, want) || c.TailTrusted() {
		t.Fatalf("trusted %v, want %v", c.Trusted, want)
	}
	if _, err := Reanchor(sink, dir, Principal{Kind: "human", Surface: "in_process"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write(Record{Kind: KindIntent, Operation: "four"}); err != nil {
		t.Fatal(err)
	}
	c, _ = Check(dir)
	if want := []bool{true, true, false, false, true, true}; !equalBools(c.Trusted, want) || !c.TailTrusted() {
		t.Fatalf("after the reanchor: trusted %v, want %v", c.Trusted, want)
	}
	if c.Err() == nil || Verify(dir) == nil {
		t.Fatal("a reanchor made the problem go away")
	}
	if !strings.Contains(c.Records[4].Note, "seq 3") {
		t.Fatalf("the reanchor does not name what it acknowledges: %q", c.Records[4].Note)
	}
	if _, err := Reanchor(sink, dir, Principal{}); !errors.Is(err, ErrNothingToReanchor) {
		t.Fatalf("a second reanchor: %v", err)
	}
}

// editLog rewrites the September log file as a same-uid process could.
func editLog(t *testing.T, dir string, edit func([]byte) []byte) {
	t.Helper()
	file := filepath.Join(dir, "2026-09.jsonl")
	data, err := os.ReadFile(file) //nolint:gosec // the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, edit(data), 0o600); err != nil { //nolint:gosec // as above
		t.Fatal(err)
	}
}

func equalBools(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type memAnchor struct {
	head   Head
	ok     bool
	stores int
}

func (m *memAnchor) Load() (Head, bool, error) { return m.head, m.ok, nil }
func (m *memAnchor) Store(h Head) error        { m.head, m.ok = h, true; m.stores++; return nil }

func anchored(t *testing.T) (string, *FileSink, *memAnchor) {
	t.Helper()
	dir := t.TempDir()
	a := &memAnchor{}
	SetAnchor(dir, a)
	t.Cleanup(func() { SetAnchor("", nil) })
	prev := AnchorInterval
	AnchorInterval = 0
	t.Cleanup(func() { AnchorInterval = prev })
	sink, err := openFileSink(dir, clock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"one", "two", "three"} {
		if _, err := sink.Write(Record{Kind: KindIntent, Operation: op}); err != nil {
			t.Fatal(err)
		}
	}
	if a.head.Seq != 4 {
		t.Fatalf("anchored at %+v", a.head)
	}
	return dir, sink, a
}

// The chain alone verifies after its last records are cut off at a line
// boundary; the anchor is what catches it, and a sink writing on does not
// move the anchor past the cut (M2).
func TestAnAnchorCatchesACutLog(t *testing.T) {
	dir, sink, a := anchored(t)
	if err := Verify(dir); err != nil {
		t.Fatal(err)
	}
	editLog(t, dir, func(data []byte) []byte {
		return bytes.Join(bytes.SplitAfter(data, []byte("\n"))[:3], nil)
	})
	SetAnchor("", nil)
	if err := Verify(dir); err != nil {
		t.Fatalf("without the anchor the cut is invisible, as it was: %v", err)
	}
	SetAnchor(dir, a)
	c, _ := Check(dir)
	if c.Err() == nil || c.TailTrusted() || c.Trusted[0] {
		t.Fatalf("a cut log was trusted: %+v", c)
	}
	// The sink writes on; the anchor stays where the log was cut from.
	if _, err := sink.Write(Record{Kind: KindBrakeChanged, Operation: "after"}); err != nil {
		t.Fatal(err)
	}
	if a.head.Seq != 4 {
		t.Fatalf("the anchor moved past the cut: %+v", a.head)
	}
	if c, _ = Check(dir); c.TailTrusted() {
		t.Fatal("writing on laundered the cut")
	}
	// A person reanchors; that moves it, and what follows is trusted.
	if _, err := Reanchor(sink, dir, Principal{Kind: "human"}); err != nil {
		t.Fatal(err)
	}
	if c, _ = Check(dir); !c.TailTrusted() || c.Err() != nil {
		t.Fatalf("after the reanchor: %v", c.Err())
	}
}

// A log rewritten with every hash recomputed chains correctly; its anchored
// record is not the one written there.
func TestAnAnchorCatchesARewrittenLog(t *testing.T) {
	dir, _, a := anchored(t)
	file := filepath.Join(dir, "2026-09.jsonl")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	SetAnchor("", nil)
	forger, err := openFileSink(dir, clock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"uno", "dos", "tres"} {
		if _, err := forger.Write(Record{Kind: KindIntent, Operation: op}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Verify(dir); err != nil {
		t.Fatalf("the forged chain should verify on its own: %v", err)
	}
	SetAnchor(dir, a)
	if err := Verify(dir); err == nil || !strings.Contains(err.Error(), "anchored seq 4") {
		t.Fatalf("a rewritten log verified against its anchor: %v", err)
	}
}
