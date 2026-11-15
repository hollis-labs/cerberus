package audit

import (
	"errors"
	"os"
	"testing"
)

// A Digester and a FileSink over one directory digest alike, so applied digests stay
// comparable once the chain opens.
func TestDigesterMatchesTheSinkKey(t *testing.T) {
	dir := t.TempDir()
	d, err := OpenDigester(dir)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := OpenFileSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := map[string]any{"id": "web", "config": map[string]any{"port": 8080}}
	if d.Digest(v) != sink.Digest(v) {
		t.Fatalf("digester %s, sink %s", d.Digest(v), sink.Digest(v))
	}
	other, err := OpenDigester(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if other.Digest(v) == d.Digest(v) {
		t.Fatal("two keys digested alike")
	}
}

// The fingerprint follows the key, and an existing-only open never creates one.
func TestDigesterFingerprintAndExistingOpen(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenExistingDigester(dir); !errors.Is(err, ErrNoDigestKey) {
		t.Fatalf("no key: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("an existing-only open created %v", entries)
	}
	created, err := OpenDigester(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := OpenExistingDigester(dir)
	if err != nil || again.Fingerprint() != created.Fingerprint() || created.Fingerprint() == "" {
		t.Fatalf("same key: %q %q %v", created.Fingerprint(), again.Fingerprint(), err)
	}
	other, err := OpenDigester(t.TempDir())
	if err != nil || other.Fingerprint() == created.Fingerprint() {
		t.Fatalf("two keys share a fingerprint: %v", err)
	}
}
