package credentialbroker

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// countingRandom counts the entropy reads: one per generated credential.
type countingRandom struct{ reads atomic.Int32 }

func (c *countingRandom) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return rand.Read(p)
}

// Two issuances racing for the same id through the default FileRecorder (the old build's
// exclusive Mkdir reservation): exactly one reserves and proceeds to dispatch; the other is
// refused at the reservation, before any credential is generated, written or dispatched; and
// the id's directory holds exactly one preparing receipt.
func TestConcurrentIssuanceReservesTheFileReceiptOnce(t *testing.T) {
	const racers = 2
	root := filepath.Join(t.TempDir(), "broker")
	id := strings.Repeat("e", 32)
	bind := binding()
	bind.ID = id
	names := []string{"runtime", "migration"}
	random := &countingRandom{}

	start := make(chan struct{})
	var wg sync.WaitGroup
	dispatches := make([]*Dispatch, racers)
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := New(root)
			b.random = random
			<-start
			dispatches[i], errs[i] = b.Prepare(context.Background(), bind, names)
		}()
	}
	close(start)
	wg.Wait()

	var winner *Dispatch
	for i := range racers {
		switch {
		case errs[i] == nil:
			if winner != nil {
				t.Fatal("two concurrent issuances both reserved the same id")
			}
			winner = dispatches[i]
		case dispatches[i] != nil:
			t.Fatalf("a refused issuance came back with a dispatch: %v", errs[i])
		case !strings.Contains(errs[i].Error(), "already reserved"):
			t.Fatalf("the losing issuance was refused for another reason than the reservation: %v", errs[i])
		}
	}
	if winner == nil {
		t.Fatalf("no issuance reserved the id: %v", errs)
	}
	// Only the winner generated credentials: one entropy read per name, none for the loser.
	if got := int(random.reads.Load()); got != len(names) {
		t.Fatalf("%d credentials generated, want the winner's %d alone", got, len(names))
	}
	issued := filepath.Join(root, "secrets", "issued", id)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(issued, name)) //nolint:gosec // the test's own file
		if err != nil || strings.TrimSpace(string(data)) != winner.Values()[name] {
			t.Fatalf("issued secret %s is not the winner's (%v)", name, err)
		}
	}
	if entries, _ := os.ReadDir(issued); len(entries) != len(names) {
		t.Fatalf("issued secrets = %v, want the winner's %v alone", entries, names)
	}
	if err := winner.Start(); err != nil {
		t.Fatalf("the winner could not dispatch: %v", err)
	}
	if err := winner.Finish(true); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, id))
	if err != nil {
		t.Fatal(err)
	}
	var receipts []string
	for _, e := range entries {
		receipts = append(receipts, e.Name())
	}
	sort.Strings(receipts)
	if strings.Join(receipts, ",") != "completed.json,dispatching.json,prepared.json,preparing.json" {
		t.Fatalf("receipts for %s = %v, want one preparing and the winner's lifecycle", id, receipts)
	}
}
