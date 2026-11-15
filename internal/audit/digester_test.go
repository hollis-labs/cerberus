package audit

import "testing"

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
