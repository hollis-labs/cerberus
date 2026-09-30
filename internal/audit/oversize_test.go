package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The re-review's repro (H-e): a caller's argument of 900 KiB of "<" grew
// six-fold in JSON into one line over the reader's limit, and from then on
// nothing could read the log: Check and Reanchor failed with "token too
// long". The field is now cut when the record is written, and a record
// over MaxRecordBytes is refused.
func TestACallersFieldCannotMakeAnUnreadableRecord(t *testing.T) {
	dir := t.TempDir()
	sink, err := openFileSink(dir, clock(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("<", 900<<10)
	fields := map[string]string{"container": huge}
	rec, err := sink.Write(Record{Kind: KindIntent, Connector: "docker", Operation: "logs", Target: Target{Kind: "docker.container", Fields: fields}, Reason: huge})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Target.Fields["container"]) > MaxFieldBytes+64 || !strings.Contains(rec.Target.Fields["container"], "more bytes not recorded") {
		t.Fatalf("the field was not cut: %d bytes", len(rec.Target.Fields["container"]))
	}
	if len(fields["container"]) != len(huge) {
		t.Fatal("cutting the record changed the caller's map")
	}
	if err := Verify(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write(Record{Kind: KindOutcome, PolicySnapshot: strings.Repeat("x", MaxRecordBytes)}); err == nil {
		t.Fatal("a record over MaxRecordBytes was written")
	}
}

// A line over the limit already in the log — written by an older build,
// or by hand — is one damaged line: the log stays readable, the problem is
// reported, what follows is not vouched for, and a person can reanchor.
func TestAnOverlongLineDoesNotMakeTheLogUnreadable(t *testing.T) {
	dir := t.TempDir()
	sink, err := openFileSink(dir, clock(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sink.Write(Record{Kind: KindIntent, Operation: "before"}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "2026-09.jsonl")
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"v":1,"note":"` + strings.Repeat("<", MaxLineBytes) + `"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err = sink.Write(Record{Kind: KindIntent, Operation: "after"}); err != nil {
		t.Fatal(err)
	}
	c, err := Check(dir)
	if err != nil {
		t.Fatalf("the log is unreadable again: %v", err)
	}
	if c.Err() == nil || c.TailTrusted() || !strings.Contains(c.Err().Error(), "torn line") {
		t.Fatalf("the overlong line was not reported as a damaged line: %+v", c.Problems)
	}
	if _, err := Reanchor(sink, dir, Principal{Kind: "human"}); err != nil {
		t.Fatalf("reanchor: %v", err)
	}
	if c, _ = Check(dir); !c.TailTrusted() {
		t.Fatal("after the reanchor the tail is not trusted")
	}
}
