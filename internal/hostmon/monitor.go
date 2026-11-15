package hostmon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

const hostListReloadEvery = 10 * time.Second

// Recorder writes one monitor event (status changes, the first failure of a streak, the
// hourly summary, a compaction) to the audit log as automation. nil records nothing; audit is
// wired in slice 6.
type Recorder func(ctx context.Context, what, note string)

// Options build a Monitor.
type Options struct {
	Prober    *Prober
	Store     SampleStore
	Telemetry *Telemetry
	Settings  hosts.MonitorSettings
	Record    Recorder
	Logger    *slog.Logger
	Now       func() time.Time
	Rand      func() float64
}

// Monitor probes monitored hosts on a schedule, keeps their samples and derived status, and
// records state changes. It never ranks or picks a host: it reports facts about each one. It
// is main's cerbapi.HostMonitor, moved; its writes go through the Telemetry writer, so a
// failing Telemetry store holds them in memory (bounded) and never stops a probe.
type Monitor struct {
	o      Options
	global chan struct{}
	ssh    chan struct{}

	mu       sync.Mutex
	running  bool
	hostList []hosts.Host
	loadedAt time.Time
	due      map[string]time.Time
	inFlight map[string]bool
	skipped  map[string]bool
	last     map[string]hosts.HostStatus // the last status written, for when the store cannot be read
	counters counters
	lastSumm time.Time
	wg       sync.WaitGroup
}

type counters struct{ Attempts, Failures, Changes int }

// New builds a monitor. It does not start until Run.
func New(o Options) *Monitor {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Rand == nil {
		o.Rand = rand.Float64
	}
	if o.Telemetry == nil {
		o.Telemetry = NewTelemetry(0, o.Logger)
	}
	return &Monitor{o: o, global: make(chan struct{}, max(1, o.Settings.Concurrency)), ssh: make(chan struct{}, max(1, o.Settings.SSHConcurrency)),
		due: map[string]time.Time{}, inFlight: map[string]bool{}, skipped: map[string]bool{}, last: map[string]hosts.HostStatus{}}
}

// Settings returns the effective settings.
func (m *Monitor) Settings() hosts.MonitorSettings { return m.o.Settings }

// Run ticks once a second until ctx ends, starting due probes. It returns when every probe in
// flight has finished.
func (m *Monitor) Run(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.lastSumm = m.o.Now()
	m.mu.Unlock()
	m.o.Logger.Info("daemon.host_monitor.start", "outpost_interval", m.o.Settings.OutpostInterval.String(),
		"ssh_interval", m.o.Settings.SSHInterval.String(), "concurrency", m.o.Settings.Concurrency)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.wg.Wait()
			m.mu.Lock()
			m.running = false
			m.mu.Unlock()
			m.o.Logger.Info("daemon.host_monitor.stop")
			return
		case <-t.C:
			m.tick(ctx)
		}
	}
}

// tick starts every probe that is due. A host is probed at most once at a time; a host whose
// last probe is still running is skipped.
func (m *Monitor) tick(ctx context.Context) {
	now := m.o.Now()
	m.mu.Lock()
	if m.hostList == nil || now.Sub(m.loadedAt) >= hostListReloadEvery {
		f, err := m.o.Prober.Load()
		if err != nil {
			m.o.Logger.Warn("daemon.host_monitor.hosts_unreadable", "error", redact.Text(err.Error()))
		} else {
			m.hostList, m.loadedAt = f.Hosts, now
			keep := map[string]bool{}
			for _, h := range f.Hosts {
				keep[h.Name] = true
			}
			for name := range m.due {
				if !keep[name] {
					delete(m.due, name)
				}
			}
			m.o.Telemetry.Write(ctx, "forget_status", "", func(ctx context.Context) error { return m.o.Store.ForgetStatus(ctx, keep) })
		}
	}
	var start []hosts.Host
	for _, h := range m.hostList {
		interval, ok := m.o.Settings.IntervalFor(h)
		if !ok {
			delete(m.due, h.Name)
			continue
		}
		if !m.o.Prober.CanReach(h) {
			// An ssh host without the ssh connector: not probed, and never marked offline for it.
			if !m.skipped[h.Name] {
				m.skipped[h.Name] = true
				m.o.Logger.Warn("daemon.host_monitor.host_skipped", "host", h.Name, "reason", ErrNoSSH.Error())
			}
			delete(m.due, h.Name)
			continue
		}
		d, seen := m.due[h.Name]
		if !seen {
			// First sight: stagger over the interval so a restart does not probe every host at once.
			m.due[h.Name] = now.Add(hosts.StartDelay(interval, m.o.Rand))
			continue
		}
		if now.Before(d) || m.inFlight[h.Name] {
			continue
		}
		m.inFlight[h.Name] = true
		start = append(start, h)
	}
	summary := now.Sub(m.lastSumm) >= time.Hour
	var c counters
	if summary {
		c, m.counters, m.lastSumm = m.counters, counters{}, now
	}
	m.mu.Unlock()

	if summary {
		m.record(ctx, "summary", fmt.Sprintf("probes=%d failed=%d status_changes=%d", c.Attempts, c.Failures, c.Changes))
	}
	for _, h := range start {
		m.wg.Add(1)
		go func(h hosts.Host) {
			defer m.wg.Done()
			m.probeOne(ctx, h)
		}(h)
	}
}

// probeOne takes a slot (global, and the ssh one for ssh hosts), probes, and folds the result
// into the host's samples and status.
func (m *Monitor) probeOne(ctx context.Context, h hosts.Host) {
	interval, _ := m.o.Settings.IntervalFor(h)
	defer func() {
		m.mu.Lock()
		delete(m.inFlight, h.Name)
		m.mu.Unlock()
	}()
	select {
	case m.global <- struct{}{}:
		defer func() { <-m.global }()
	case <-ctx.Done():
		return
	}
	if h.Reach.EffectiveTransport() == "ssh" {
		select {
		case m.ssh <- struct{}{}:
			defer func() { <-m.ssh }()
		case <-ctx.Done():
			return
		}
	}

	started := m.o.Now()
	pctx, cancel := context.WithTimeout(ctx, m.o.Settings.ProbeTimeout)
	view, err := m.o.Prober.Probe(pctx, h.Name)
	cancel()
	finished := m.o.Now()
	ms := finished.Sub(started).Milliseconds()

	obs := hosts.Observation{At: finished}
	switch {
	case err != nil:
		obs.Err = trimReason(redact.Text(err.Error()))
	case view.State == hosts.StateOnline && view.Probed != nil && view.ProbedAt != nil && !view.ProbedAt.Before(started.Add(-time.Second)):
		obs.OK = true
		s := hosts.SampleFrom(h.Name, finished, view.Probed, ms)
		obs.Sample = &s
		obs.Partial = view.Probed.MemoryTotal == 0 || view.Probed.DiskTotal == 0
	default:
		obs.Err = trimReason(view.Reason)
		if obs.Err == "" {
			obs.Err = "no usable answer"
		}
	}

	th := m.o.Settings.Thresholds
	if obs.OK {
		sample := *obs.Sample
		m.o.Telemetry.Write(ctx, "sample", h.Name, func(ctx context.Context) error { return m.o.Store.AppendSample(ctx, sample) })
	}
	prev, _, err := m.o.Store.Status(ctx, h.Name)
	if err != nil {
		// The store cannot be read: continue from the last status this process wrote.
		m.mu.Lock()
		prev = m.last[h.Name]
		m.mu.Unlock()
	}
	prev.Host = h.Name
	recent, _ := m.o.Store.Recent(ctx, h.Name, max(th.LoadSamples, th.SlowProbeSamples))
	next, changed := th.Advance(prev, obs, recent, m.o.Settings.ProbeTimeout)
	next.Host = h.Name
	due := finished.Add(hosts.NextDelay(interval, next.ConsecutiveFailures, m.o.Settings.MaxBackoff, m.o.Settings.Jitter, m.o.Rand))
	next.NextDue = &due
	m.o.Telemetry.Write(ctx, "status", h.Name, func(ctx context.Context) error { return m.o.Store.PutStatus(ctx, next) })

	m.mu.Lock()
	m.last[h.Name] = next
	m.due[h.Name] = due
	m.counters.Attempts++
	if !obs.OK {
		m.counters.Failures++
	}
	if changed {
		m.counters.Changes++
	}
	m.mu.Unlock()

	if changed {
		ev := hosts.Event{At: finished, Host: h.Name, Kind: hosts.EventStatusChanged, From: orUnknown(prev.Status), To: next.Status,
			Expected: h.Labels["intermittent"] == "true"}
		for _, r := range next.Reasons {
			ev.Reasons = append(ev.Reasons, r.Code)
		}
		m.o.Telemetry.Write(ctx, "event", h.Name, func(ctx context.Context) error {
			_, err := m.o.Store.AppendEvent(ctx, ev)
			return err
		})
	}
	switch {
	case changed:
		m.o.Logger.Info("daemon.host_monitor.status_changed", "host", h.Name, "from", prev.Status, "to", next.Status)
		m.record(ctx, "status_changed", fmt.Sprintf("%s %s -> %s %s", h.Name, orUnknown(prev.Status), next.Status, reasonCodes(next.Reasons)))
	case !obs.OK && next.ConsecutiveFailures == 1:
		m.record(ctx, "probe_failed", fmt.Sprintf("%s first failed probe of a streak: %s", h.Name, obs.Err))
	}
}

func orUnknown(s string) string {
	if s == "" {
		return hosts.StatusUnknown
	}
	return s
}

func reasonCodes(rs []hosts.Reason) string {
	var c []string
	for _, r := range rs {
		c = append(c, r.Code)
	}
	sort.Strings(c)
	return strings.Join(c, ",")
}

func trimReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func (m *Monitor) record(ctx context.Context, what, note string) {
	if m.o.Record != nil {
		m.o.Record(ctx, what, note)
	}
}

// RunJanitor compacts samples hourly (first run a minute after start): it builds the rollups
// that are missing and removes what retention allows, only below the rollups that count the
// rows (see hosts.SampleStore.Compact). It runs whether or not probing is enabled, so
// retention never depends on the monitor being on. Each run first retries held telemetry.
func (m *Monitor) RunJanitor(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.o.Telemetry.Flush(ctx)
			m.CompactOnce(ctx)
			timer.Reset(time.Hour)
		}
	}
}

// CompactOnce runs one compaction, logs what it did (every removal is counted), and records it
// when it did anything.
func (m *Monitor) CompactOnce(ctx context.Context) {
	rep, err := m.o.Store.Compact(ctx, m.o.Now(), m.o.Settings.Retention())
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			m.o.Logger.Warn("daemon.host_monitor.compact_failed", "error", redact.Text(err.Error()))
		}
		return
	}
	if rep.Empty() {
		return
	}
	m.o.Logger.Info("daemon.host_monitor.compacted", "rollups_written", rep.RollupsWritten, "raw_deleted", rep.RawDeleted,
		"rollup_5m_deleted", rep.Rollup5mDeleted, "rollup_1h_deleted", rep.Rollup1hDeleted)
	m.record(ctx, "compacted", rep.String())
}
