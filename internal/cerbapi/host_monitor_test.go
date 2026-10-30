package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/store/sqlite"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// monBackend answers every probe from its own connection object, so probes of
// different hosts can overlap, and counts how many run at once.
type monBackend struct {
	fakeSSHBackend
	rig *monitorRig
}

func (b *monBackend) Connect(ctx context.Context, host string, port int, user, keyFile string, hk sshconn.HostKeyConfig) error {
	if b.rig.down.Load() {
		return errors.New("dial tcp: i/o timeout")
	}
	return nil
}

func (b *monBackend) Exec(ctx context.Context, command string) (*sshconn.ExecResult, error) {
	n := b.rig.running.Add(1)
	for {
		m := b.rig.maxRunning.Load()
		if n <= m || b.rig.maxRunning.CompareAndSwap(m, n) {
			break
		}
	}
	b.rig.execs.Add(1)
	if gate := b.rig.gate; gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}
	time.Sleep(b.rig.delay)
	b.rig.running.Add(-1)
	return &sshconn.ExecResult{Stdout: b.rig.stdout}, nil
}

type monitorRig struct {
	svc        *HostService
	mon        *HostMonitor
	store      *hosts.SampleStore
	sink       *audit.Memory
	clock      *fakeClock
	path       string
	down       atomic.Bool
	running    atomic.Int32
	maxRunning atomic.Int32
	execs      atomic.Int32
	gate       chan struct{}
	delay      time.Duration
	stdout     string
}

func newMonitorRig(t *testing.T, hostsYAML string, mutate func(*hosts.MonitorSettings)) *monitorRig {
	t.Helper()
	r := &monitorRig{stdout: strings.Replace(goodProbeOutput, "disk_kb=1000000 500000", "disk_kb=200000000 100000000", 1), clock: &fakeClock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}, sink: audit.NewMemory()}
	dir := t.TempDir()
	r.path = filepath.Join(dir, "hosts.yaml")
	if err := os.WriteFile(r.path, []byte(hostsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := func(id string) (*config.ResourceDef, bool) {
		return &config.ResourceDef{ID: id, Name: id, Type: "server", Connector: "ssh", Config: map[string]any{"host": "ssh://probe@192.0.2.10", "key_file": "/nonexistent/key"}}, true
	}
	conn := sshconn.NewWithBackendFactory(nil, func() sshconn.Backend { return &monBackend{rig: r} })
	r.svc = NewHostService(r.sink, HostServiceOptions{HostsPath: r.path, StatePath: "-", Resources: lookup,
		SSH: func(context.Context) (HostSSH, error) { return conn, nil }, Now: r.clock.Now})
	db, err := sqlite.Open(filepath.Join(dir, "cerberus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r.store = hosts.NewSampleStore(db.DB())
	set := hosts.DefaultMonitorSettings()
	set.Enabled = true
	if mutate != nil {
		mutate(&set)
	}
	r.mon = NewHostMonitor(r.svc, r.store, set, nil)
	r.mon.now = r.clock.Now
	r.mon.rnd = func() float64 { return 0.5 }
	r.svc.AttachMonitor(r.mon)
	return r
}

func (r *monitorRig) idle(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		r.mon.mu.Lock()
		n := len(r.mon.inFlight)
		r.mon.mu.Unlock()
		if n == 0 {
			r.mon.wg.Wait()
			return
		}
	}
	t.Fatal("probes never finished")
}

func (r *monitorRig) tick(t *testing.T) {
	t.Helper()
	r.mon.tick(context.Background())
	r.idle(t)
}

func (r *monitorRig) hostRecords() map[string]int {
	out := map[string]int{}
	for _, rec := range r.sink.Records() {
		if rec.Connector == "host" && string(rec.Kind) == "intent" {
			out[rec.Operation]++
		}
	}
	return out
}

const oneSSHHost = `version: 1
hosts:
  - {name: box-a, role: worker, reach: {ssh: box-a-ssh}}
`

func TestMonitorStaggersJittersAndRecordsASample(t *testing.T) {
	r := newMonitorRig(t, oneSSHHost, nil)
	r.tick(t) // first sight: scheduled, not probed (the start is staggered over the interval)
	if r.execs.Load() != 0 {
		t.Fatal("the first tick must only schedule")
	}
	r.clock.Advance(time.Minute)
	r.tick(t)
	if r.execs.Load() != 0 {
		t.Fatal("probed before its staggered start")
	}
	r.clock.Advance(2 * time.Minute) // 3m: past the 2.5m stagger
	r.tick(t)
	if r.execs.Load() != 1 {
		t.Fatalf("execs = %d", r.execs.Load())
	}
	st, ok, _ := r.store.Status(context.Background(), "box-a")
	if !ok || st.Status != hosts.StatusOnline || st.NextDue == nil || !st.NextDue.Equal(r.clock.Now().Add(5*time.Minute)) {
		t.Fatalf("status: %+v", st)
	}
	if ss, _ := r.store.Samples(context.Background(), "box-a", time.Time{}, time.Time{}, 0); len(ss) != 1 || ss[0].CPU != 4 || ss[0].MemoryAvailable == 0 {
		t.Fatalf("samples: %+v", ss)
	}
	// Not due again until its interval has passed.
	r.clock.Advance(4 * time.Minute)
	r.tick(t)
	if r.execs.Load() != 1 {
		t.Fatal("probed again before the interval")
	}
	r.clock.Advance(2 * time.Minute)
	r.tick(t)
	if r.execs.Load() != 2 {
		t.Fatalf("execs = %d", r.execs.Load())
	}
}

func TestMonitorBacksOffAndAuditsOnlyStreaksAndChanges(t *testing.T) {
	r := newMonitorRig(t, oneSSHHost, func(s *hosts.MonitorSettings) { s.SSHInterval = time.Minute })
	r.tick(t)
	r.clock.Advance(time.Minute)
	r.tick(t) // first successful probe
	r.down.Store(true)
	var gaps []time.Duration
	last := r.clock.Now()
	for i := 0; i < 3; i++ {
		st, _, _ := r.store.Status(context.Background(), "box-a")
		r.clock.Advance(st.NextDue.Sub(r.clock.Now()))
		r.tick(t)
		gaps = append(gaps, r.clock.Now().Sub(last))
		last = r.clock.Now()
	}
	st, _, _ := r.store.Status(context.Background(), "box-a")
	if st.Status != hosts.StatusOffline || st.ConsecutiveFailures != 3 {
		t.Fatalf("status: %+v", st)
	}
	if gaps[1] < gaps[0] || gaps[2] < gaps[1] {
		t.Fatalf("a failing host must be probed less often: %v", gaps)
	}
	if st.NextDue.Sub(r.clock.Now()) != 8*time.Minute {
		t.Fatalf("after 3 failures the next probe is interval x 2^3: %v", st.NextDue.Sub(r.clock.Now()))
	}
	recs := r.hostRecords()
	if recs["probe"] != 0 {
		t.Fatalf("routine probes must not be audited one by one: %v", recs)
	}
	events := 0
	for _, rec := range r.sink.Records() {
		if rec.Operation == OpMonitorEvent && string(rec.Kind) == "outcome" && !strings.HasPrefix(rec.Reason, "summary") {
			events++
		}
	}
	if events != 3 { // first status (unknown->online), streak start, online->offline; the hourly summary is separate
		t.Fatalf("monitor events: %d (%v)", events, recs)
	}
	notes := ""
	for _, rec := range r.sink.Records() {
		notes += rec.Reason + "|"
	}
	for _, want := range []string{"status_changed", "probe_failed"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("no %q record in %s", want, notes)
		}
	}
	// Recovery on the first success.
	r.down.Store(false)
	r.clock.Advance(9 * time.Minute)
	r.tick(t)
	st, _, _ = r.store.Status(context.Background(), "box-a")
	if st.Status != hosts.StatusOnline || st.ConsecutiveFailures != 0 {
		t.Fatalf("recovery: %+v", st)
	}
}

func manyHosts(n int) string {
	var b strings.Builder
	b.WriteString("version: 1\nhosts:\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "  - {name: box-%02d, role: worker, reach: {ssh: box-a-ssh}}\n", i)
	}
	return b.String()
}

func TestMonitorHonorsTheConcurrencyCapsAndNeverStacksAHost(t *testing.T) {
	r := newMonitorRig(t, manyHosts(6), func(s *hosts.MonitorSettings) { s.Concurrency, s.SSHConcurrency = 3, 2 })
	r.delay = 30 * time.Millisecond
	r.tick(t)
	r.clock.Advance(6 * time.Minute)
	r.tick(t)
	if got := r.execs.Load(); got != 6 {
		t.Fatalf("every due host is probed: %d", got)
	}
	if m := r.maxRunning.Load(); m > 2 {
		t.Fatalf("ssh concurrency cap is 2, saw %d at once", m)
	}

	// A host whose probe is still running is skipped, not stacked.
	r2 := newMonitorRig(t, oneSSHHost, nil)
	r2.gate = make(chan struct{})
	r2.tick(t)
	r2.clock.Advance(6 * time.Minute)
	r2.mon.tick(context.Background())
	for deadline := time.Now().Add(2 * time.Second); r2.execs.Load() < 1 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
	}
	r2.clock.Advance(10 * time.Minute)
	r2.mon.tick(context.Background())
	r2.mon.tick(context.Background())
	time.Sleep(50 * time.Millisecond)
	if r2.execs.Load() != 1 {
		t.Fatalf("a host with a probe in flight must not get a second: %d", r2.execs.Load())
	}
	close(r2.gate)
	r2.idle(t)
}

func TestMonitorSkipsUnmonitoredHosts(t *testing.T) {
	r := newMonitorRig(t, `version: 1
hosts:
  - {name: off, reach: {ssh: box-a-ssh}, monitor: {enabled: false}}
  - {name: none}
  - {name: on, reach: {ssh: box-a-ssh}, monitor: {interval: 20s}}
`, nil)
	r.tick(t)
	r.clock.Advance(30 * time.Second)
	r.tick(t)
	if r.execs.Load() != 1 {
		t.Fatalf("only the monitored host is probed: %d", r.execs.Load())
	}
	snap := r.mon.Snapshot(context.Background())
	if len(snap.Hosts) != 1 || snap.Hosts[0].Name != "on" || snap.Hosts[0].Interval != "20s" {
		t.Fatalf("snapshot: %+v", snap.Hosts)
	}
}

func TestStatusReadsAndStaleness(t *testing.T) {
	r := newMonitorRig(t, oneSSHHost+"  - {name: never, role: worker, reach: {ssh: box-a-ssh}, monitor: {interval: 1h}}\n", nil)
	ctx := context.Background()
	r.tick(t)
	r.clock.Advance(3 * time.Minute)
	r.mon.rnd = func() float64 { return 0 }
	r.tick(t)
	v, err := r.svc.HostStatus(ctx, "box-a")
	if err != nil || v.Status.Status != hosts.StatusOnline || !v.Monitored || v.Interval != "5m0s" || v.Thresholds.OfflineAfter != 3 {
		t.Fatalf("status: %+v %v", v, err)
	}
	if nv, _ := r.svc.HostStatus(ctx, "never"); nv.Status.Status != hosts.StatusUnknown || nv.Status.Reasons[0].Code != "never_probed" {
		t.Fatalf("a host the monitor has not reached reads unknown: %+v", nv.Status)
	}
	hv, _ := r.svc.Get(ctx, "box-a")
	if hv.Status == nil || hv.Status.Status != hosts.StatusOnline {
		t.Fatalf("HostView.Status: %+v", hv.Status)
	}
	// 20 minutes with no probe (the daemon was down): stale, never kept as online.
	r.clock.Advance(20 * time.Minute)
	v, _ = r.svc.HostStatus(ctx, "box-a")
	if v.Status.Status != hosts.StatusUnknown || !v.Stale || v.Status.Reasons[0].Code != "stale" {
		t.Fatalf("stale: %+v", v)
	}
	all, err := r.svc.AllHostStatus(ctx)
	if err != nil || len(all) != 2 || all[0].Name != "box-a" {
		t.Fatalf("all: %+v %v", all, err)
	}
	ss, err := r.svc.HostSamples(ctx, "box-a", SamplesQuery{})
	if err != nil || len(ss) != 1 {
		t.Fatalf("samples: %+v %v", ss, err)
	}
	if _, err := r.svc.HostSamples(ctx, "box-a", SamplesQuery{Grain: "1h"}); err == nil {
		t.Fatal("HostSamples is raw only; a rollup grain goes through HostRollups")
	}
	if _, err := r.svc.HostSamples(ctx, "ghost", SamplesQuery{}); err == nil {
		t.Fatal("unknown host")
	}
	snap, err := r.svc.MonitorState(ctx)
	if err != nil || !snap.Enabled || len(snap.Hosts) != 2 {
		t.Fatalf("monitor: %+v %v", snap, err)
	}
}

func TestMonitoringReadsWithoutAMonitorSaySo(t *testing.T) {
	f := newHostFixture(t, &probeBackend{})
	if _, err := f.svc.HostStatus(context.Background(), "box-a"); err == nil || !strings.Contains(err.Error(), "monitoring is not available") {
		t.Fatalf("err = %v", err)
	}
	v, _ := f.svc.Get(context.Background(), "box-a")
	if v.Status != nil {
		t.Fatal("a host without a monitor has no status, not a healthy one")
	}
}

func TestMonitorOverTheSocketAndNoRanking(t *testing.T) {
	r := newMonitorRig(t, oneSSHHost, nil)
	r.tick(t)
	r.clock.Advance(3 * time.Minute)
	r.tick(t)
	client := serveOnSocket(t, NewInProcessClient(WithHostService(r.svc)))
	ctx := context.Background()
	v, err := client.HostStatus(ctx, "box-a")
	if err != nil || v.Status.Status != hosts.StatusOnline {
		t.Fatalf("%+v %v", v, err)
	}
	if all, err := client.AllHostStatus(ctx); err != nil || len(all) != 1 {
		t.Fatalf("%+v %v", all, err)
	}
	if ss, err := client.HostSamples(ctx, "box-a", SamplesQuery{Since: r.clock.Now().Add(-time.Hour), Limit: 10}); err != nil || len(ss) != 1 {
		t.Fatalf("%+v %v", ss, err)
	}
	if snap, err := client.MonitorState(ctx); err != nil || len(snap.Hosts) != 1 {
		t.Fatalf("%+v %v", snap, err)
	}
	// No score, rank or "best" anywhere in the status or monitor payloads.
	for _, raw := range []any{v, r.mon.Snapshot(ctx)} {
		b := fmt.Sprintf("%+v", raw)
		for _, banned := range []string{"score", "rank", "best"} {
			if strings.Contains(strings.ToLower(b), banned) {
				t.Fatalf("%q in %s", banned, b)
			}
		}
	}
}

func TestJanitorCompactsAndServesRollups(t *testing.T) {
	r := newMonitorRig(t, oneSSHHost, func(s *hosts.MonitorSettings) {
		s.SampleRetention, s.Rollup5mRetention, s.Rollup1hRetention = time.Hour, 48*time.Hour, 96*time.Hour
	})
	ctx := context.Background()
	base := r.clock.Now()
	for i := 0; i < 12; i++ {
		l := float64(i)
		if err := r.store.AppendSample(ctx, hosts.Sample{Host: "box-a", At: base.Add(time.Duration(i) * time.Minute), Load1: &l, CPU: 4, MemoryAvailable: 1 << 30, DiskFree: 10 << 30}); err != nil {
			t.Fatal(err)
		}
	}
	r.clock.Advance(3 * time.Hour)
	r.mon.compactOnce(ctx)
	rs, err := r.svc.HostRollups(ctx, "box-a", "5m", SamplesQuery{})
	if err != nil || len(rs) != 3 || rs[0].N != 5 || rs[2].N != 2 {
		t.Fatalf("5m rollups: %+v %v", rs, err)
	}
	if raw, _ := r.svc.HostSamples(ctx, "box-a", SamplesQuery{}); len(raw) != 0 {
		t.Fatalf("raw samples past retention and covered by rollups should be gone: %d", len(raw))
	}
	if _, err := r.svc.HostRollups(ctx, "box-a", "2h", SamplesQuery{}); err == nil {
		t.Fatal("an unknown grain was accepted")
	}
	if _, err := r.svc.HostSamples(ctx, "box-a", SamplesQuery{Grain: "1h"}); err == nil {
		t.Fatal("HostSamples must not return rollups")
	}
	found := false
	for _, rec := range r.sink.Records() {
		if rec.Operation == OpMonitorEvent && strings.HasPrefix(rec.Reason, "compacted") {
			found = true
		}
	}
	if !found {
		t.Fatal("a compaction that did something must be recorded")
	}
	r.mon.compactOnce(ctx) // nothing new: nothing recorded again
	client := serveOnSocket(t, NewInProcessClient(WithHostService(r.svc)))
	got, err := client.HostRollups(ctx, "box-a", "5m", SamplesQuery{Limit: 2})
	if err != nil || len(got) != 2 || got[1].N != 2 {
		t.Fatalf("over the socket: %+v %v", got, err)
	}
}
