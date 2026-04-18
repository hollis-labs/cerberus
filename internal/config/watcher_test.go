package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls fn() every 10ms up to timeout. Used for fsnotify tests
// where the OS event plumbing takes a non-deterministic amount of time.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestWatcherFiresOnWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nservices: []\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var count atomic.Int32
	w, err := NewWatcher(path, func() { count.Add(1) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.SetDebounce(30 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = w.Run(ctx)
	}()

	// Give the watcher a moment to attach.
	time.Sleep(50 * time.Millisecond)

	// One write → one fire.
	if err := os.WriteFile(path, []byte("version: 1\nservices: [{id: a}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 2*time.Second, func() bool { return count.Load() >= 1 }) {
		t.Fatalf("watcher did not fire within 2s; count=%d", count.Load())
	}
	firstFire := count.Load()

	cancel()
	wg.Wait()
	// Debounce protects us from double-fire on editor saves; we only
	// assert "fired at least once."
	if firstFire < 1 {
		t.Fatalf("expected >=1 fires, got %d", firstFire)
	}
}

func TestWatcherDebouncesBurstOfWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var count atomic.Int32
	w, err := NewWatcher(path, func() { count.Add(1) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Long debounce so a rapid burst coalesces into a single fire.
	w.SetDebounce(200 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = w.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	// Fire 5 writes within the debounce window.
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(path, []byte("version: 1\n#"+time.Now().String()+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for the debounce window to elapse + a little slack.
	time.Sleep(400 * time.Millisecond)
	got := count.Load()
	cancel()
	wg.Wait()

	if got != 1 {
		t.Fatalf("expected exactly 1 fire for a burst, got %d", got)
	}
}

func TestWatcherIgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	sibling := filepath.Join(dir, "other.yaml")
	if err := os.WriteFile(target, []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var count atomic.Int32
	w, err := NewWatcher(target, func() { count.Add(1) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.SetDebounce(30 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = w.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	// Touch a sibling file — should not fire.
	if err := os.WriteFile(sibling, []byte("irrelevant"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if count.Load() != 0 {
		t.Fatalf("watcher fired on unrelated file (count=%d)", count.Load())
	}

	cancel()
	wg.Wait()
}

func TestNewWatcherValidatesArgs(t *testing.T) {
	if _, err := NewWatcher("", func() {}, nil); err == nil {
		t.Error("expected error for empty path")
	}
	if _, err := NewWatcher("/tmp/x", nil, nil); err == nil {
		t.Error("expected error for nil callback")
	}
}
