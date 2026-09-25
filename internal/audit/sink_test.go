package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func readRecords(t *testing.T, file string) []Record {
	t.Helper()
	data, err := os.ReadFile(file) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	var out []Record
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unparseable line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func clock(t time.Time) func() time.Time { return func() time.Time { return t } }

// The first write opens the chain; files are 0600 in a 0700 directory; every
// record chains to the one before it.
func TestFileSinkChainsAndRestricts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	sink, err := openFileSink(dir, clock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := sink.Write(Record{Kind: KindIntent, Connector: "docker", Operation: "list_containers"}); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(dir, "2026-09.jsonl")
	recs := readRecords(t, file)
	if len(recs) != 4 || recs[0].Kind != KindChainStart {
		t.Fatalf("records = %+v, want chain_start then 3", recs)
	}
	for i, rec := range recs {
		if rec.Seq != uint64(i+1) || rec.Posture != PostureSecure || rec.Version != SchemaVersion {
			t.Errorf("record %d: %+v", i, rec)
		}
	}
	if err := Verify(dir); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: dirMode, file: fileMode, filepath.Join(dir, keyName): fileMode} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode %o, want %o", path, got, want)
		}
	}
}

// One writer: concurrent writes from many goroutines, through two sinks on
// the same directory — as the daemon and an in-process CLI would be — leave
// one unbroken chain with no lost or duplicated sequence number.
func TestFileSinkConcurrentWritersKeepOneChain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	now := clock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	a, err := openFileSink(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := openFileSink(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	const perWriter = 25
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		sink := a
		if w%2 == 1 {
			sink = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := sink.Write(Record{Kind: KindIntent}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := Verify(dir); err != nil {
		t.Fatal(err)
	}
	if got, want := len(readRecords(t, filepath.Join(dir, "2026-09.jsonl"))), 16*perWriter+1; got != want {
		t.Fatalf("%d records, want %d", got, want)
	}
}

// A month boundary starts a new file whose first record names the previous
// file, and the chain runs across it. A sink reopened later continues it.
func TestFileSinkRotatesMonthlyAndChainsAcrossFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	sept, err := openFileSink(dir, clock(time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if _, werr := sept.Write(Record{Kind: KindIntent}); werr != nil {
		t.Fatal(werr)
	}
	oct, err := openFileSink(dir, clock(time.Date(2026, 10, 1, 0, 1, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oct.Write(Record{Kind: KindIntent}); err != nil {
		t.Fatal(err)
	}
	recs := readRecords(t, filepath.Join(dir, "2026-10.jsonl"))
	if recs[0].Kind != KindFileStart || recs[0].PrevFile != "2026-09.jsonl" || recs[0].Seq != 3 {
		t.Fatalf("October opens with %+v, want file_start naming 2026-09.jsonl at seq 3", recs[0])
	}
	if err := Verify(dir); err != nil {
		t.Fatal(err)
	}
}

// Editing, removing or reordering a record breaks the chain, and Verify says
// where.
func TestVerifyDetectsTampering(t *testing.T) {
	for name, tamper := range map[string]func([]byte) []byte{
		"edit": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"operation":"stop"`), []byte(`"operation":"list"`), 1)
		},
		"remove": func(b []byte) []byte {
			lines := bytes.Split(b, []byte{'\n'})
			return bytes.Join(append(lines[:2:2], lines[3:]...), []byte{'\n'})
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "audit")
			sink, err := openFileSink(dir, clock(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)))
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range []string{"stop", "start", "stop"} {
				if _, err := sink.Write(Record{Kind: KindIntent, Operation: op}); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(dir, "2026-09.jsonl")
			data, _ := os.ReadFile(file) //nolint:gosec // test file
			if err := os.WriteFile(file, tamper(data), fileMode); err != nil {
				t.Fatal(err)
			}
			if err := Verify(dir); err == nil {
				t.Fatal("tampering went undetected")
			}
		})
	}
}

// A torn last write — a crash mid-line — is not repaired in place: the next
// write records a chain_break after it and resumes from the last complete
// record, and the chain still verifies.
func TestFileSinkRecordsATornWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	now := clock(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
	sink, err := openFileSink(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, werr := sink.Write(Record{Kind: KindIntent}); werr != nil {
		t.Fatal(werr)
	}
	file := filepath.Join(dir, "2026-09.jsonl")
	f, _ := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, fileMode) //nolint:gosec // test file
	_, _ = f.WriteString(`{"v":1,"seq":3,"kind":"inte`)
	_ = f.Close()

	restarted, err := openFileSink(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Write(Record{Kind: KindIntent}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file) //nolint:gosec // test file
	if !bytes.Contains(data, []byte(`"kind":"chain_break"`)) || !bytes.Contains(data, []byte(`"kind":"inte`)) {
		t.Fatalf("torn write not recorded, or rewritten:\n%s", data)
	}
	if err := Verify(dir); err != nil {
		t.Fatal(err)
	}
}

// The args digest is keyed per directory: stable for equal arguments, never
// the arguments themselves, and not reproducible without the key.
func TestDigestIsKeyed(t *testing.T) {
	a, _ := OpenFileSink(filepath.Join(t.TempDir(), "a"))
	b, _ := OpenFileSink(filepath.Join(t.TempDir(), "b"))
	args := map[string]any{"password": "hunter2-SENTINEL", "id": "x"}
	if a.Digest(args) != a.Digest(map[string]any{"id": "x", "password": "hunter2-SENTINEL"}) {
		t.Fatal("digest depends on key order")
	}
	if a.Digest(args) == b.Digest(args) {
		t.Fatal("two directories share a digest key")
	}
	if strings.Contains(a.Digest(args), "hunter2") || !strings.HasPrefix(a.Digest(args), "hmac-sha256:") {
		t.Fatalf("digest %q", a.Digest(args))
	}
}
