package hosts_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/store/sqlite"
)

func newStore(t *testing.T) *hosts.SampleStore {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "cerberus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return hosts.NewSampleStore(s.DB())
}

func TestSampleStoreRoundTrip(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	l := 0.5
	for i := 0; i < 5; i++ {
		if err := st.AppendSample(ctx, hosts.Sample{Host: "a", At: base.Add(time.Duration(i) * time.Minute), Load1: &l, CPU: 4, MemoryTotal: 8, MemoryAvailable: 4, DiskTotal: 100, DiskFree: 50, ProbeMS: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AppendSample(ctx, hosts.Sample{Host: "b", At: base}); err != nil {
		t.Fatal(err)
	}
	recent, err := st.Recent(ctx, "a", 3)
	if err != nil || len(recent) != 3 || !recent[0].At.After(recent[1].At) || recent[0].ProbeMS != 4 || *recent[0].Load1 != 0.5 {
		t.Fatalf("recent: %+v %v", recent, err)
	}
	all, err := st.Samples(ctx, "a", time.Time{}, time.Time{}, 0)
	if err != nil || len(all) != 5 || !all[0].At.Before(all[4].At) {
		t.Fatalf("samples must come in time order: %+v %v", all, err)
	}
	win, _ := st.Samples(ctx, "a", base.Add(time.Minute), base.Add(3*time.Minute), 0)
	if len(win) != 3 {
		t.Fatalf("window: %d", len(win))
	}
	if capped, _ := st.Samples(ctx, "a", time.Time{}, time.Time{}, 2); len(capped) != 2 || capped[1].ProbeMS != 4 {
		t.Fatalf("limit keeps the newest: %+v", capped)
	}
	if none, _ := st.Samples(ctx, "nobody", time.Time{}, time.Time{}, 0); none == nil || len(none) != 0 {
		t.Fatalf("no samples must be an empty list, not null: %#v", none)
	}
	if nilLoad := recent; nilLoad == nil {
		t.Fatal("unreachable")
	}
	b, _ := st.Samples(ctx, "b", time.Time{}, time.Time{}, 0)
	if b[0].Load1 != nil {
		t.Fatal("an absent load must stay absent, not become 0")
	}
}

func TestStatusStore(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	if _, ok, err := st.Status(ctx, "a"); ok || err != nil {
		t.Fatalf("no status yet: %v %v", ok, err)
	}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	due := now.Add(time.Minute)
	in := hosts.HostStatus{Host: "a", Status: hosts.StatusDegraded, Since: now, LastOK: &now, LastAttempt: &now, NextDue: &due,
		Reasons: []hosts.Reason{{Code: "disk_low", Detail: "7% free"}}, ConsecutiveFailures: 1, Candidate: "online", CandidateCount: 1}
	if err := st.PutStatus(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := st.PutStatus(ctx, hosts.HostStatus{Host: "gone", Status: hosts.StatusOnline, Since: now}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Status(ctx, "a")
	if err != nil || !ok || got.Status != hosts.StatusDegraded || got.Reasons[0].Code != "disk_low" || !got.NextDue.Equal(due) || got.CandidateCount != 1 || got.ConsecutiveFailures != 1 {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if err := st.ForgetStatus(ctx, map[string]bool{"a": true}); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.AllStatus(ctx); len(all) != 1 || all["a"].Host != "a" {
		t.Fatalf("forget: %+v", all)
	}
}
