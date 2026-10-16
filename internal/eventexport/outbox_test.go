package eventexport

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testEnvelope(kind string) Envelope {
	return Envelope{Kind: kind, Time: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), Subject: "msg://x/cerberus/1", Source: Source{Channel: ChannelAudit}}
}

func TestOutboxAssignsSequenceGenerationAndIDsOnceAndIsIdempotentByKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	o, err := OpenFileOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := o.Append("a/1", SourceRef{Source: "audit"}, testEnvelope("audit.recorded"))
	if err != nil || !created || first.Seq != 1 || first.Envelope.Sequence != 1 || !strings.HasPrefix(first.Envelope.ID, "evt_") || len(first.Envelope.ID) != 36 {
		t.Fatalf("first = %+v created=%v err=%v", first, created, err)
	}
	if first.Envelope.Generation == 0 || first.Envelope.Generation != o.Generation() || first.Envelope.SchemaVersion != "1" {
		t.Fatalf("generation/schema = %+v", first.Envelope)
	}
	again, created, err := o.Append("a/1", SourceRef{Source: "audit"}, testEnvelope("audit.recorded"))
	if err != nil || created || again.Envelope.ID != first.Envelope.ID || again.Seq != 1 {
		t.Fatalf("a replay of the same source record must change nothing: %+v created=%v err=%v", again, created, err)
	}
	second, _, err := o.Append("a/2", SourceRef{Source: "audit"}, testEnvelope("audit.recorded"))
	if err != nil || second.Seq != 2 || second.Prev == "" || second.Envelope.ID == first.Envelope.ID {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if info, _ := os.Stat(filepath.Join(dir, outboxFile)); info.Mode().Perm() != 0o600 {
		t.Fatalf("outbox mode %v", info.Mode())
	}

	// Reopened: same generation, same ids, same keys, the sequence continues.
	reopened, err := OpenFileOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Generation() != o.Generation() || !reopened.Has("a/2") {
		t.Fatalf("reopened generation %d has(a/2)=%v", reopened.Generation(), reopened.Has("a/2"))
	}
	third, _, err := reopened.Append("a/3", SourceRef{Source: "audit"}, testEnvelope("audit.recorded"))
	if err != nil || third.Seq != 3 {
		t.Fatalf("third = %+v, %v", third, err)
	}
	got, err := reopened.After(1, 0)
	if err != nil || len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 3 {
		t.Fatalf("After(1) = %+v, %v", got, err)
	}
	head, ok, _ := reopened.Head()
	if !ok || head.Seq != 3 {
		t.Fatalf("head = %+v", head)
	}
}

func TestOutboxTruncatesATornFinalLineAndCarriesOn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	o, err := OpenFileOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a/1", "a/2"} {
		if _, _, err = o.Append(key, SourceRef{Source: "audit"}, testEnvelope("audit.recorded")); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, outboxFile)
	good, _ := os.ReadFile(path)                                                            //nolint:gosec // the test's own file
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)                               //nolint:gosec // the test's own file
	_, _ = f.WriteString(`{"seq":3,"prev":"sha256:x","key":"a/3","env":{"kind":"audit.rec`) // a crash mid-append
	_ = f.Close()

	reopened, err := OpenFileOutbox(dir)
	if err != nil {
		t.Fatalf("a torn final line must be repaired, not refused: %v", err)
	}
	after, _ := os.ReadFile(path) //nolint:gosec // the test's own file
	if string(after) != string(good) {
		t.Fatalf("the torn line was not cut off cleanly:\n%s", after)
	}
	if reopened.Has("a/3") {
		t.Fatal("the torn entry was kept")
	}
	e, created, err := reopened.Append("a/3", SourceRef{Source: "audit"}, testEnvelope("audit.recorded"))
	if err != nil || !created || e.Seq != 3 {
		t.Fatalf("export resumes: %+v created=%v err=%v", e, created, err)
	}
}

func TestOutboxRefusesWhatItCannotSafelyExtend(t *testing.T) {
	build := func(t *testing.T) (string, string) {
		dir := filepath.Join(t.TempDir(), "outbox")
		o, err := OpenFileOutbox(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"a/1", "a/2", "a/3"} {
			if _, _, err = o.Append(key, SourceRef{Source: "audit"}, testEnvelope("audit.recorded")); err != nil {
				t.Fatal(err)
			}
		}
		return dir, filepath.Join(dir, outboxFile)
	}
	t.Run("entries but no generation file (a restore)", func(t *testing.T) {
		dir, _ := build(t)
		if err := os.Remove(filepath.Join(dir, generationFile)); err != nil { //nolint:gosec // the test's own file
			t.Fatal(err)
		}
		if _, err := OpenFileOutbox(dir); !errors.Is(err, ErrRefused) {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, generationFile)); err == nil {
			t.Fatal("a new generation was written over entries from the old one")
		}
	})
	t.Run("a damaged middle line", func(t *testing.T) {
		_, path := build(t)
		data, _ := os.ReadFile(path) //nolint:gosec // the test's own file
		lines := strings.Split(string(data), "\n")
		lines[1] = "garbage"
		_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600) //nolint:gosec // the test's own file
		if _, err := OpenFileOutbox(filepath.Dir(path)); !errors.Is(err, ErrRefused) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a removed middle line", func(t *testing.T) {
		_, path := build(t)
		data, _ := os.ReadFile(path) //nolint:gosec // the test's own file
		lines := strings.Split(string(data), "\n")
		_ = os.WriteFile(path, []byte(strings.Join(append(lines[:1:1], lines[2:]...), "\n")), 0o600) //nolint:gosec // the test's own file
		if _, err := OpenFileOutbox(filepath.Dir(path)); !errors.Is(err, ErrRefused) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("entries from another generation", func(t *testing.T) {
		dir, _ := build(t)
		_ = os.WriteFile(filepath.Join(dir, generationFile), []byte("12345\n"), 0o600) //nolint:gosec // the test's own file
		if _, err := OpenFileOutbox(dir); !errors.Is(err, ErrRefused) {
			t.Fatalf("err = %v", err)
		}
	})
}

// Moving a refused outbox aside is how an operator starts over: the new outbox
// has a different generation, so no id or sequence is shared with the old one.
func TestAFreshOutboxGetsANewGeneration(t *testing.T) {
	a, err := OpenFileOutbox(filepath.Join(t.TempDir(), "a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenFileOutbox(filepath.Join(t.TempDir(), "b"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Generation() == b.Generation() {
		t.Fatalf("two outboxes share generation %d", a.Generation())
	}
}
