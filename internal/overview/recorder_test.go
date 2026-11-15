package overview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

// The recorder persists every tick, reloads the buffer on start and drops what is past
// retention.
func TestRecorderPersistsReloadsAndEvicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "overview_snapshots.json")
	old := OverviewSnapshot{Timestamp: time.Now().Add(-8 * 24 * time.Hour), Resources: 9}
	recent := OverviewSnapshot{Timestamp: time.Now().Add(-time.Hour), Resources: 3}
	raw, _ := json.Marshal([]OverviewSnapshot{old, recent})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	take := func(context.Context) (OverviewSnapshot, error) {
		n.Add(1)
		return OverviewSnapshot{Timestamp: time.Now(), Resources: 5, Running: 2}, nil
	}
	r := NewSnapshotRecorder(SnapshotRecorderConfig{Interval: time.Hour, Path: path}, take, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	got := r.Recent(30 * 24 * time.Hour)
	if len(got) != 2 || got[0].Resources != 3 || got[1].Resources != 5 {
		t.Fatalf("buffer %+v: want the recent stored sample then the new one, the 8-day-old one evicted", got)
	}
	var onDisk []OverviewSnapshot
	b, err := os.ReadFile(path) //nolint:gosec // the test's own file
	if err != nil || json.Unmarshal(b, &onDisk) != nil || len(onDisk) != 2 {
		t.Fatalf("on disk: %s %v", b, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi, err)
	}
}

func TestBucketGaugeTrendForwardFills(t *testing.T) {
	now := time.Now()
	samples := []OverviewSnapshot{
		{Timestamp: now.Add(-23*time.Hour - 30*time.Minute), Resources: 4},
		{Timestamp: now.Add(-2*time.Hour - 30*time.Minute), Resources: 6},
	}
	got := BucketGaugeTrend(samples, func(s OverviewSnapshot) int { return s.Resources }, 24*time.Hour, 24)
	if got[0] != 4 || got[10] != 4 || got[21] != 6 || got[23] != 6 {
		t.Fatalf("trend %v", got)
	}
}
