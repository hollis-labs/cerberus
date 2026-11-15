package audit

import "testing"

func TestSwitchDivertsWritesAndKeepsThePrimaryDigest(t *testing.T) {
	primary, alt := NewMemory(), NewMemory()
	sw := NewSwitch(primary)
	args := map[string]any{"id": "x"}
	digest := sw.Digest(args)
	if _, err := sw.Write(Record{Kind: KindIntent, Operation: "one"}); err != nil {
		t.Fatal(err)
	}
	sw.Divert(alt)
	if !sw.Diverted() {
		t.Fatal("not diverted")
	}
	if _, err := sw.Write(Record{Kind: KindIntent, Operation: "two"}); err != nil {
		t.Fatal(err)
	}
	if sw.Digest(args) != digest {
		t.Fatal("the digest key changed with the diversion")
	}
	sw.Divert(nil)
	if _, err := sw.Write(Record{Kind: KindIntent, Operation: "three"}); err != nil {
		t.Fatal(err)
	}
	ops := func(m *Memory) (out []string) {
		for _, r := range m.Records() {
			out = append(out, r.Operation)
		}
		return out
	}
	if p, a := ops(primary), ops(alt); len(p) != 2 || p[0] != "one" || p[1] != "three" || len(a) != 1 || a[0] != "two" {
		t.Fatalf("primary %v, diverted %v", p, a)
	}
	if sw.Primary() != primary {
		t.Fatal("primary")
	}
}
