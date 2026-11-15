package auditchain

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

// copyOldChain copies the fixture the old build wrote (testdata/oldchain, see its generator)
// into dir, and returns the head the old build anchored.
func copyOldChain(t *testing.T, dir string) audit.Head {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var head audit.Head
	entries, err := os.ReadDir(filepath.Join("testdata", "oldchain"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join("testdata", "oldchain", e.Name())) //nolint:gosec // test fixtures
		if err != nil {
			t.Fatal(err)
		}
		if e.Name() == "anchor.json" {
			if err := json.Unmarshal(raw, &head); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), raw, 0o600); err != nil { //nolint:gosec // the test's own temp dir
			t.Fatal(err)
		}
	}
	return head
}

// The new build continues the old build's chain: the old records stay byte for byte, the new
// ones follow at the next seq in the same format, the anchor (seeded with the old head under the
// new directory's path, as the migration writes it) moves, and the chain verifies end to end.
// CERBERUS_CONTINUED_CHAIN_OUT keeps the result, for the old binary's audit verify
// (scripts/verify-continued-chain.sh).
func TestNewContinuesTheOldChain(t *testing.T) {
	secrets.MockStoreForTests()
	dir := filepath.Join(t.TempDir(), "audit")
	if out := os.Getenv("CERBERUS_CONTINUED_CHAIN_OUT"); out != "" {
		dir = out
	}
	head := copyOldChain(t, dir)
	oldBytes, err := os.ReadFile(filepath.Join(dir, head.File)) //nolint:gosec // the test's own dir
	if err != nil {
		t.Fatal(err)
	}
	if err := (KeychainAnchor{Dir: dir}).Store(head); err != nil {
		t.Fatal(err)
	}
	sink, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := audit.Principal{Kind: "human", Surface: "web", Via: "web", SelfReported: false}
	var last audit.Record
	for _, op := range []string{"apply", "stop"} {
		in, err := sink.Write(audit.Record{Kind: audit.KindIntent, Connector: "local", Operation: op, Principal: p,
			Target: audit.Target{Kind: "local.resource", Resource: "web"}, ArgsDigest: sink.Digest(map[string]any{"id": "web"})})
		if err != nil {
			t.Fatal(err)
		}
		if last, err = sink.Write(audit.Record{Kind: audit.KindOutcome, OperationID: in.OperationID, Connector: "local", Operation: op, Principal: p, Decision: "allowed"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sink.Write(audit.Record{Kind: audit.KindApprovalDecided, Connector: "approvals", Operation: "decide", Principal: p}); err != nil {
		t.Fatal(err)
	}
	if last.Seq <= head.Seq {
		t.Fatalf("the new records did not continue at the old head: seq %d after head %d", last.Seq, head.Seq)
	}
	now, err := os.ReadFile(filepath.Join(dir, head.File)) //nolint:gosec // as above
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(now, oldBytes) {
		t.Fatal("the old records changed")
	}
	checked, err := audit.Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := checked.Err(); err != nil {
		t.Fatalf("the continued chain does not verify: %v", err)
	}
	moved, ok, err := (KeychainAnchor{Dir: dir}).Load()
	if err != nil || !ok || moved.Seq <= head.Seq {
		t.Fatalf("the anchor did not move past the old head: %+v %v %v", moved, ok, err)
	}
	if out := os.Getenv("CERBERUS_CONTINUED_CHAIN_OUT"); out != "" {
		raw, _ := json.Marshal(moved)
		if err := os.WriteFile(filepath.Join(filepath.Dir(out), "anchor.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A chain whose anchor names a head that is gone fails, so the check above has teeth.
	if err := (KeychainAnchor{Dir: dir}).Store(audit.Head{Seq: moved.Seq + 5, Hash: "sha256:" + strings.Repeat("0", 64), File: head.File}); err != nil {
		t.Fatal(err)
	}
	if bad, err := audit.Check(dir); err == nil && bad.Err() == nil {
		t.Fatal("a chain behind its anchor verified")
	}
	if err := (KeychainAnchor{Dir: dir}).Store(moved); err != nil {
		t.Fatal(err)
	}
}
