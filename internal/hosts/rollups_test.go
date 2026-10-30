package hosts_test

import (
	"context"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
)

var day0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func put(t *testing.T, st *hosts.SampleStore, host string, at time.Time, load *float64, memAvail, diskFree, probeMS int64) {
	t.Helper()
	if err := st.AppendSample(context.Background(), hosts.Sample{Host: host, At: at, Load1: load, CPU: 4, MemoryTotal: 8 << 30,
		MemoryAvailable: memAvail, DiskTotal: 100 << 30, DiskFree: diskFree, UptimeSeconds: 1000, ProbeMS: probeMS}); err != nil {
		t.Fatal(err)
	}
}

func f(v float64) *float64 { return &v }

func TestRollupsAreExactAndOnlyForCompleteBuckets(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	// Bucket 12:00-12:05: three samples. Bucket 12:05-12:10: one (the last, incomplete at 12:07).
	put(t, st, "a", day0.Add(12*time.Hour+30*time.Second), f(1.0), 4<<30, 50<<30, 100)
	put(t, st, "a", day0.Add(12*time.Hour+90*time.Second), f(3.0), 2<<30, 40<<30, 300)
	put(t, st, "a", day0.Add(12*time.Hour+200*time.Second), nil, 6<<30, 45<<30, 200)
	put(t, st, "a", day0.Add(12*time.Hour+5*time.Minute+10*time.Second), f(9), 1<<30, 10<<30, 50)
	now := day0.Add(12*time.Hour + 7*time.Minute)
	rep, err := st.Compact(ctx, now, hosts.Retention{})
	if err != nil || rep.RollupsWritten != 1 {
		t.Fatalf("only the finished bucket is rolled up: %+v %v", rep, err)
	}
	rs, err := st.Rollups(ctx, "a", hosts.Grain5m, time.Time{}, time.Time{}, 0)
	if err != nil || len(rs) != 1 {
		t.Fatalf("%+v %v", rs, err)
	}
	r := rs[0]
	if r.N != 3 || r.Load1 == nil || r.Load1.Min != 1 || r.Load1.Max != 3 || r.Load1.Avg != 2 {
		t.Fatalf("load must average only the samples that reported one: %+v", r.Load1)
	}
	if r.MemoryAvailable.Min != float64(2<<30) || r.MemoryAvailable.Max != float64(6<<30) || r.MemoryAvailable.Avg != float64(4<<30) {
		t.Fatalf("memory: %+v", r.MemoryAvailable)
	}
	if r.DiskFree.Min != float64(40<<30) || r.ProbeMS.Avg != 200 || r.ProbeMS.Max != 300 || r.CPU != 4 {
		t.Fatalf("disk/probe: %+v %+v", r.DiskFree, r.ProbeMS)
	}
	// Idempotent: a second run at the same time changes nothing.
	if rep, _ := st.Compact(ctx, now, hosts.Retention{}); !rep.Empty() {
		t.Fatalf("second run: %+v", rep)
	}
	// Once the second bucket is complete it is rolled up too.
	rep, _ = st.Compact(ctx, day0.Add(12*time.Hour+11*time.Minute), hosts.Retention{})
	if rep.RollupsWritten != 1 {
		t.Fatalf("%+v", rep)
	}
}

func TestGrainsChainAndCombineExactly(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	total, sum := 0, 0.0
	for i := 0; i < 48*12; i++ { // two days, one sample per 5 minutes
		l := float64(i % 7)
		put(t, st, "a", day0.Add(time.Duration(i)*5*time.Minute+time.Second), f(l), int64(i+1)<<20, 50<<30, 100)
		total++
		sum += l
	}
	now := day0.Add(3 * 24 * time.Hour)
	if _, err := st.Compact(ctx, now, hosts.Retention{}); err != nil {
		t.Fatal(err)
	}
	for grain, want := range map[string]int{hosts.Grain5m: 48 * 12, hosts.Grain1h: 48, hosts.Grain1d: 2} {
		rs, err := st.Rollups(ctx, "a", grain, time.Time{}, time.Time{}, 1000)
		if err != nil || len(rs) != want {
			t.Fatalf("%s: %d rollups, want %d (%v)", grain, len(rs), want, err)
		}
		n := 0
		avgSum := 0.0
		for _, r := range rs {
			n += r.N
			avgSum += r.Load1.Avg * float64(r.N)
		}
		if n != total || int(avgSum+0.5) != int(sum+0.5) {
			t.Fatalf("%s: samples counted %d (want %d), load sum %.1f (want %.1f): a rollup must count exactly what it covers", grain, n, total, avgSum, sum)
		}
	}
	day, _ := st.Rollups(ctx, "a", hosts.Grain1d, time.Time{}, time.Time{}, 10)
	if day[0].MemoryAvailable.Min != float64(1<<20) || day[0].MemoryAvailable.Max != float64(288<<20) {
		t.Fatalf("a daily rollup's extremes come from the raw samples: %+v", day[0].MemoryAvailable)
	}
	if _, err := st.Rollups(ctx, "a", "2h", time.Time{}, time.Time{}, 0); err == nil {
		t.Fatal("unknown grain accepted")
	}
}

func TestRetentionNeverDeletesBeforeARollupCountsTheRows(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	old := day0.Add(2*time.Hour + time.Minute)
	put(t, st, "a", old, f(1), 1, 1, 1)
	put(t, st, "a", old.Add(time.Minute), f(2), 1, 1, 1)
	rawOnly := hosts.Retention{Raw: time.Minute}

	// The bucket is still open: although retention says the rows are old,
	// nothing may be removed because no rollup counts them yet.
	if rep, err := st.Compact(ctx, old.Add(2*time.Minute), rawOnly); err != nil || rep.RawDeleted != 0 {
		t.Fatalf("raw rows deleted before any rollup covered them: %+v %v", rep, err)
	}
	if rows, _ := st.Samples(ctx, "a", time.Time{}, time.Time{}, 0); len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	// Once the bucket is complete and rolled up, the raw rows go, and the
	// rollup that counted them stays.
	rep, err := st.Compact(ctx, day0.Add(2*time.Hour+10*time.Minute), rawOnly)
	if err != nil || rep.RawDeleted != 2 || rep.RollupsWritten == 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if rows, _ := st.Samples(ctx, "a", time.Time{}, time.Time{}, 0); len(rows) != 0 {
		t.Fatalf("raw rows remain: %d", len(rows))
	}
	rs, _ := st.Rollups(ctx, "a", hosts.Grain5m, time.Time{}, time.Time{}, 0)
	if len(rs) != 1 || rs[0].N != 2 {
		t.Fatalf("the rollup must keep what the raw rows said: %+v", rs)
	}
	// 5m rows go only once the 1h tier counts them: the hour is open at 2:10,
	// complete at 3:00.
	with5m := hosts.Retention{Raw: time.Minute, Rollup5m: time.Minute}
	if rep, _ := st.Compact(ctx, day0.Add(2*time.Hour+30*time.Minute), with5m); rep.Rollup5mDeleted != 0 {
		t.Fatalf("5m rows deleted before the hour counted them: %+v", rep)
	}
	rep, err = st.Compact(ctx, day0.Add(3*time.Hour+time.Minute), with5m)
	if err != nil || rep.Rollup5mDeleted != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if h, _ := st.Rollups(ctx, "a", hosts.Grain1h, time.Time{}, time.Time{}, 0); len(h) != 1 || h[0].N != 2 {
		t.Fatalf("hourly: %+v", h)
	}
	// 1h rows go once the day is counted; daily rollups are kept forever.
	all := hosts.Retention{Raw: time.Minute, Rollup5m: time.Minute, Rollup1h: time.Minute}
	if rep, _ := st.Compact(ctx, day0.Add(12*time.Hour), all); rep.Rollup1hDeleted != 0 {
		t.Fatalf("1h rows deleted before the day counted them: %+v", rep)
	}
	rep, err = st.Compact(ctx, day0.Add(25*time.Hour), all)
	if err != nil || rep.Rollup1hDeleted != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if d, _ := st.Rollups(ctx, "a", hosts.Grain1d, time.Time{}, time.Time{}, 0); len(d) != 1 || d[0].N != 2 {
		t.Fatalf("daily: %+v", d)
	}
}

func TestAHostWithNoLoadHasNoLoadStat(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	put(t, st, "a", day0.Add(time.Second), nil, 1<<30, 1<<30, 5)
	if _, err := st.Compact(ctx, day0.Add(time.Hour), hosts.Retention{}); err != nil {
		t.Fatal(err)
	}
	rs, _ := st.Rollups(ctx, "a", hosts.Grain5m, time.Time{}, time.Time{}, 0)
	if len(rs) != 1 || rs[0].Load1 != nil {
		t.Fatalf("an absent load must stay absent, not become 0: %+v", rs)
	}
}
