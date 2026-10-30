package hosts

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func ok(at time.Time, mut func(*Sample)) Observation {
	l := 0.1
	s := Sample{Host: "h", At: at, CPU: 4, Load1: &l, MemoryTotal: 8 << 30, MemoryAvailable: 4 << 30, DiskTotal: 100 << 30, DiskFree: 50 << 30, ProbeMS: 200}
	if mut != nil {
		mut(&s)
	}
	return Observation{At: at, OK: true, Sample: &s}
}

func fail(at time.Time) Observation { return Observation{At: at, Err: "dial timeout"} }

func run(t *testing.T, th Thresholds, obs ...Observation) (HostStatus, []bool) {
	t.Helper()
	var st HostStatus
	var changes []bool
	var recent []Sample
	for _, o := range obs {
		if o.Sample != nil {
			recent = append([]Sample{*o.Sample}, recent...)
		}
		var c bool
		st, c = th.Advance(st, o, recent, 45*time.Second)
		changes = append(changes, c)
	}
	return st, changes
}

func TestNeverProbedBecomesOnlineAtOnce(t *testing.T) {
	st, ch := run(t, DefaultMonitorSettings().Thresholds, ok(t0, nil))
	if st.Status != StatusOnline || !ch[0] || st.LastOK == nil || len(st.Reasons) != 0 {
		t.Fatalf("%+v %v", st, ch)
	}
}

func TestOfflineNeedsThreeFailuresAndRecoversOnOne(t *testing.T) {
	th := DefaultMonitorSettings().Thresholds
	st, ch := run(t, th, ok(t0, nil), fail(t0.Add(time.Minute)), fail(t0.Add(2*time.Minute)))
	if st.Status != StatusOnline || st.ConsecutiveFailures != 2 || ch[1] || ch[2] {
		t.Fatalf("two failures must not flip the host: %+v %v", st, ch)
	}
	if len(st.Reasons) != 1 || st.Reasons[0].Code != "probe_failed" {
		t.Fatalf("the streak must be visible: %+v", st.Reasons)
	}
	st, ch = run(t, th, ok(t0, nil), fail(t0.Add(1)), fail(t0.Add(2)), fail(t0.Add(3)))
	if st.Status != StatusOffline || !ch[3] || st.Reasons[0].Code != "unreachable" {
		t.Fatalf("three failures: %+v %v", st, ch)
	}
	st, ch = run(t, th, ok(t0, nil), fail(t0.Add(1)), fail(t0.Add(2)), fail(t0.Add(3)), ok(t0.Add(4), nil))
	if st.Status != StatusOnline || !ch[4] || st.ConsecutiveFailures != 0 {
		t.Fatalf("one success recovers: %+v %v", st, ch)
	}
}

func TestDegradedNeedsTwoConsistentResultsBothWays(t *testing.T) {
	th := DefaultMonitorSettings().Thresholds
	low := func(s *Sample) { s.DiskFree = 2 << 30 }
	st, ch := run(t, th, ok(t0, nil), ok(t0.Add(1), low))
	if st.Status != StatusOnline || ch[1] || st.CandidateCount != 1 || len(st.Reasons) != 0 {
		t.Fatalf("one bad result must not flip: %+v %v", st, ch)
	}
	st, ch = run(t, th, ok(t0, nil), ok(t0.Add(1), low), ok(t0.Add(2), low))
	if st.Status != StatusDegraded || !ch[2] || st.Reasons[0].Code != "disk_low" {
		t.Fatalf("two bad results: %+v %v", st, ch)
	}
	// A flapping signal never changes the status.
	st, ch = run(t, th, ok(t0, nil), ok(t0.Add(1), low), ok(t0.Add(2), nil), ok(t0.Add(3), low), ok(t0.Add(4), nil))
	if st.Status != StatusOnline || ch[1] || ch[2] || ch[3] || ch[4] {
		t.Fatalf("flapping: %+v %v", st, ch)
	}
	st, ch = run(t, th, ok(t0, nil), ok(t0.Add(1), low), ok(t0.Add(2), low), ok(t0.Add(3), nil), ok(t0.Add(4), nil))
	if st.Status != StatusOnline || !ch[4] {
		t.Fatalf("recovery needs two good results: %+v %v", st, ch)
	}
}

func TestThresholdReasons(t *testing.T) {
	th := DefaultMonitorSettings().Thresholds
	mem := func(s *Sample) { s.MemoryAvailable = 100 << 20 }
	r := th.Reasons(ok(t0, mem), []Sample{*ok(t0, mem).Sample}, 45*time.Second)
	if len(r) != 1 || r[0].Code != "memory_low" {
		t.Fatalf("%+v", r)
	}
	// Load must stay high for LoadSamples samples running.
	h := 9.0
	hi := func(s *Sample) { s.Load1 = &h }
	one := []Sample{*ok(t0, hi).Sample}
	if r := th.Reasons(ok(t0, hi), one, time.Minute); len(r) != 0 {
		t.Fatalf("one high load sample is not load_high: %+v", r)
	}
	three := []Sample{*ok(t0, hi).Sample, *ok(t0, hi).Sample, *ok(t0, hi).Sample}
	if r := th.Reasons(ok(t0, hi), three, time.Minute); len(r) != 1 || r[0].Code != "load_high" {
		t.Fatalf("%+v", r)
	}
	slow := func(s *Sample) { s.ProbeMS = 30000 }
	three = []Sample{*ok(t0, slow).Sample, *ok(t0, slow).Sample, *ok(t0, slow).Sample}
	if r := th.Reasons(ok(t0, slow), three, 45*time.Second); len(r) != 1 || r[0].Code != "probe_slow" {
		t.Fatalf("%+v", r)
	}
}

func TestStaleMeasurementReadsUnknown(t *testing.T) {
	th := DefaultMonitorSettings().Thresholds
	st, _ := run(t, th, ok(t0, nil))
	eff, stale := th.Effective(st, time.Minute, t0.Add(2*time.Minute))
	if stale || eff.Status != StatusOnline {
		t.Fatalf("fresh: %+v", eff)
	}
	eff, stale = th.Effective(st, time.Minute, t0.Add(10*time.Minute))
	if !stale || eff.Status != StatusUnknown || eff.Reasons[0].Code != "stale" {
		t.Fatalf("stale: %+v", eff)
	}
}

func TestIntervalsJitterBackoffAndStagger(t *testing.T) {
	m := DefaultMonitorSettings()
	oh := Host{Name: "a", Reach: Reach{Transport: "outpost"}}
	sh := Host{Name: "b", Reach: Reach{SSH: "x"}}
	if d, ok := m.IntervalFor(oh); !ok || d != time.Minute {
		t.Fatalf("outpost: %v %v", d, ok)
	}
	if d, ok := m.IntervalFor(sh); !ok || d != 5*time.Minute {
		t.Fatalf("ssh: %v %v", d, ok)
	}
	if _, ok := m.IntervalFor(Host{Name: "c"}); ok {
		t.Fatal("a host with no transport is not monitored")
	}
	off := false
	sh.Monitor = &Monitor{Enabled: &off}
	if _, ok := m.IntervalFor(sh); ok {
		t.Fatal("a host switched off is not monitored")
	}
	sh.Monitor = &Monitor{Interval: "1s"}
	if d, _ := m.IntervalFor(sh); d != m.MinInterval {
		t.Fatalf("the floor applies: %v", d)
	}
	mid := func() float64 { return 0.5 }
	if d := NextDelay(time.Minute, 0, 15*time.Minute, 0.1, mid); d != time.Minute {
		t.Fatalf("no jitter at 0.5: %v", d)
	}
	lo, hi := func() float64 { return 0 }, func() float64 { return 0.999999 }
	if d := NextDelay(time.Minute, 0, 15*time.Minute, 0.1, lo); d != 54*time.Second {
		t.Fatalf("-10%%: %v", d)
	}
	if d := NextDelay(time.Minute, 0, 15*time.Minute, 0.1, hi); d < 65*time.Second || d > 66*time.Second {
		t.Fatalf("+10%%: %v", d)
	}
	for n, want := range map[int]time.Duration{1: 2 * time.Minute, 2: 4 * time.Minute, 3: 8 * time.Minute, 4: 15 * time.Minute, 9: 15 * time.Minute} {
		if d := NextDelay(time.Minute, n, 15*time.Minute, 0, mid); d != want {
			t.Fatalf("failures=%d: %v, want %v", n, d, want)
		}
	}
	if d := StartDelay(time.Minute, func() float64 { return 0.25 }); d != 15*time.Second {
		t.Fatalf("stagger: %v", d)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := m
	bad.Concurrency = 0
	if bad.Validate() == nil {
		t.Fatal("zero concurrency accepted")
	}
}
