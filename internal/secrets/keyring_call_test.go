package secrets

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func withShortGate(t *testing.T) {
	t.Helper()
	oldT, oldC := KeyringTimeout, KeyringCooldown
	KeyringTimeout, KeyringCooldown = 50*time.Millisecond, 200*time.Millisecond
	ResetKeyringGate()
	t.Cleanup(func() { KeyringTimeout, KeyringCooldown = oldT, oldC; ResetKeyringGate() })
}

// A call that never returns (a locked store waiting for an unlock prompt) is
// an error after the timeout, a second call fails at once, and the gate opens
// again when the stuck call finally finishes and the cooldown has passed.
func TestKeyringCallBoundsAStuckStore(t *testing.T) {
	withShortGate(t)
	release := make(chan struct{})
	started := time.Now()
	_, err := KeyringCall("read x", func() (string, error) { <-release; return "late", nil })
	var unavailable *KeyringUnavailableError
	if !errors.As(err, &unavailable) || time.Since(started) > 2*time.Second {
		t.Fatalf("a stuck call must time out promptly with KeyringUnavailableError: %v after %v", err, time.Since(started))
	}
	calls := 0
	begin := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := KeyringCall("read y", func() (string, error) { calls++; return "", nil }); !errors.As(err, &unavailable) {
			t.Fatalf("while stuck, calls must fail fast: %v", err)
		}
	}
	if calls != 0 || time.Since(begin) > 40*time.Millisecond {
		t.Fatalf("fail-fast calls ran the store (%d) or waited (%v)", calls, time.Since(begin))
	}
	close(release)
	time.Sleep(300 * time.Millisecond) // the stuck call returned; the cooldown passed
	v, err := KeyringCall("read z", func() (string, error) { return "ok", nil })
	if err != nil || v != "ok" {
		t.Fatalf("after recovery: %q %v", v, err)
	}
}

func TestKeyringCallPassesResultsAndErrorsThrough(t *testing.T) {
	withShortGate(t)
	want := errors.New("not found")
	if _, err := KeyringCall("read a", func() (string, error) { return "", want }); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Concurrent fast calls either run or are told the store is busy; none hangs.
			_, _ = KeyringCall("read b", func() (int, error) { return 1, nil })
		}()
	}
	wg.Wait()
}
