package cerbapi

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

// Monitor and status operation names (Hosts E1). All are reads.
const (
	OpHostStatus        = "host_status"
	OpHostSamples       = "host_samples"
	OpHostMonitor       = "host_monitor"
	OpMonitorEvent      = "monitor_event"
	hostListReloadEvery = 10 * time.Second
)

// HostMonitor probes monitored hosts on a schedule, keeps their samples and
// derived status, and records state changes. It never ranks or picks a host:
// it reports facts about each one.
//
// It does not ask for the OS keyring, takes no part in request handling, and
// calls the host service directly (never the daemon's own socket), so probing
// the host the daemon runs on, over Outpost or ssh, cannot recurse.
type HostMonitor struct {
	svc      *HostService
	store    *hosts.SampleStore
	settings hosts.MonitorSettings
	logger   *slog.Logger
	now      func() time.Time
	rnd      func() float64

	global chan struct{}
	ssh    chan struct{}

	mu       sync.Mutex
	running  bool
	hostList []hosts.Host
	loadedAt time.Time
	due      map[string]time.Time
	inFlight map[string]bool
	lastSeen map[string]time.Time
	counters monitorCounters
	lastSumm time.Time
	wg       sync.WaitGroup
}

type monitorCounters struct {
	Attempts, Failures, Changes int
}

// NewHostMonitor builds a monitor. It does not start until Run.
func NewHostMonitor(svc *HostService, store *hosts.SampleStore, settings hosts.MonitorSettings, logger *slog.Logger) *HostMonitor {
	if logger == nil {
		logger = slog.Default()
	}
	return &HostMonitor{svc: svc, store: store, settings: settings, logger: logger, now: time.Now, rnd: rand.Float64,
		global: make(chan struct{}, settings.Concurrency), ssh: make(chan struct{}, settings.SSHConcurrency),
		due: map[string]time.Time{}, inFlight: map[string]bool{}, lastSeen: map[string]time.Time{}}
}

// Settings returns the effective settings.
func (m *HostMonitor) Settings() hosts.MonitorSettings { return m.settings }

// Run ticks once a second until ctx ends, starting due probes. It returns when
// every probe in flight has finished.
func (m *HostMonitor) Run(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	m.running = true
	m.lastSumm = m.now()
	m.mu.Unlock()
	m.logger.Info("daemon.host_monitor.start", "outpost_interval", m.settings.OutpostInterval.String(), "ssh_interval", m.settings.SSHInterval.String(),
		"concurrency", m.settings.Concurrency)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.wg.Wait()
			m.mu.Lock()
			m.running = false
			m.mu.Unlock()
			m.logger.Info("daemon.host_monitor.stop")
			return ctx.Err()
		case <-t.C:
			m.tick(ctx)
		}
	}
}

// tick starts every probe that is due. A host is probed at most once at a
// time; a host whose last probe is still running is skipped.
func (m *HostMonitor) tick(ctx context.Context) {
	now := m.now()
	m.mu.Lock()
	if m.hostList == nil || now.Sub(m.loadedAt) >= hostListReloadEvery {
		f, err := m.svc.load()
		if err != nil {
			m.logger.Warn("daemon.host_monitor.hosts_unreadable", "error", redact.Text(err.Error()))
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
			_ = m.store.ForgetStatus(ctx, keep)
		}
	}
	var start []hosts.Host
	for _, h := range m.hostList {
		interval, ok := m.settings.IntervalFor(h)
		if !ok {
			delete(m.due, h.Name)
			continue
		}
		d, seen := m.due[h.Name]
		if !seen {
			// First sight: stagger over the interval so a restart does not
			// probe every host at once.
			m.due[h.Name] = now.Add(hosts.StartDelay(interval, m.rnd))
			continue
		}
		if now.Before(d) || m.inFlight[h.Name] {
			continue
		}
		m.inFlight[h.Name] = true
		start = append(start, h)
	}
	summary := now.Sub(m.lastSumm) >= time.Hour
	var counters monitorCounters
	if summary {
		counters, m.counters, m.lastSumm = m.counters, monitorCounters{}, now
	}
	m.mu.Unlock()

	if summary {
		m.record(ctx, "summary", fmt.Sprintf("probes=%d failed=%d status_changes=%d", counters.Attempts, counters.Failures, counters.Changes))
	}
	for _, h := range start {
		m.wg.Add(1)
		go func(h hosts.Host) {
			defer m.wg.Done()
			m.probeOne(ctx, h)
		}(h)
	}
}

// probeOne takes a slot (global, and the ssh one for ssh hosts), probes, and
// folds the result into the host's samples and status.
func (m *HostMonitor) probeOne(ctx context.Context, h hosts.Host) {
	interval, _ := m.settings.IntervalFor(h)
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

	started := m.now()
	pctx, cancel := context.WithTimeout(ctx, m.settings.ProbeTimeout)
	view, err := m.svc.ProbeQuiet(pctx, h.Name)
	cancel()
	finished := m.now()
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

	th := m.settings.Thresholds
	if obs.OK {
		if err := m.store.AppendSample(ctx, *obs.Sample); err != nil {
			event := "daemon.host_monitor.sample_write_failed"
			if errors.Is(err, hosts.ErrLateSample) {
				event = "daemon.host_monitor.sample_late" // the clock stepped back: not stored, never lost uncounted
			}
			m.logger.Warn(event, "host", h.Name, "error", redact.Text(err.Error()))
		}
	}
	prev, _, err := m.store.Status(ctx, h.Name)
	if err != nil {
		m.logger.Warn("daemon.host_monitor.status_read_failed", "host", h.Name, "error", redact.Text(err.Error()))
	}
	prev.Host = h.Name
	recent, _ := m.store.Recent(ctx, h.Name, max(th.LoadSamples, th.SlowProbeSamples))
	next, changed := th.Advance(prev, obs, recent, m.settings.ProbeTimeout)
	next.Host = h.Name
	due := finished.Add(hosts.NextDelay(interval, next.ConsecutiveFailures, m.settings.MaxBackoff, m.settings.Jitter, m.rnd))
	next.NextDue = &due
	if err := m.store.PutStatus(ctx, next); err != nil {
		m.logger.Warn("daemon.host_monitor.status_write_failed", "host", h.Name, "error", redact.Text(err.Error()))
	}

	m.mu.Lock()
	m.due[h.Name] = due
	m.lastSeen[h.Name] = finished
	m.counters.Attempts++
	if !obs.OK {
		m.counters.Failures++
	}
	if changed {
		m.counters.Changes++
	}
	m.mu.Unlock()

	switch {
	case changed:
		m.logger.Info("daemon.host_monitor.status_changed", "host", h.Name, "from", prev.Status, "to", next.Status)
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

// record writes one audit record for a monitor event, as automation: the
// monitor is Cerberus acting on its own, not a caller.
func (m *HostMonitor) record(ctx context.Context, what, note string) {
	def, _ := HostDefinition().Operation(OpMonitorEvent)
	spec := auditSpec{connector: "host", operation: OpMonitorEvent, op: def, known: true, automation: true,
		config: map[string]any{"event": what}, resources: m.svc.resources}
	call, err := beginGated(ctx, m.svc.audit, m.logger, spec)
	if err != nil {
		m.logger.Warn("daemon.host_monitor.audit_failed", "event", what, "error", redact.Text(err.Error()))
		return
	}
	call.note(what + ": " + note)
	call.finish(nil)
}

// RunJanitor compacts samples hourly (first run a minute after start): it
// builds the rollups that are missing and removes what retention allows, only
// below the rollups that count the rows (see hosts.SampleStore.Compact). It
// runs whether or not probing is enabled, so retention never depends on the
// monitor being on.
func (m *HostMonitor) RunJanitor(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.compactOnce(ctx)
			timer.Reset(time.Hour)
		}
	}
}

// compactOnce runs one compaction, logs what it did (every removal is
// counted), and records it when it did anything.
func (m *HostMonitor) compactOnce(ctx context.Context) {
	rep, err := m.store.Compact(ctx, m.now(), m.settings.Retention())
	if err != nil {
		m.logger.Warn("daemon.host_monitor.compact_failed", "error", redact.Text(err.Error()))
		return
	}
	if rep.Empty() {
		return
	}
	m.logger.Info("daemon.host_monitor.compacted", "rollups_written", rep.RollupsWritten, "raw_deleted", rep.RawDeleted,
		"rollup_5m_deleted", rep.Rollup5mDeleted, "rollup_1h_deleted", rep.Rollup1hDeleted)
	m.record(ctx, "compacted", rep.String())
}

// MonitorHostState is one host's place in the schedule.
type MonitorHostState struct {
	Name                string     `json:"name"`
	Transport           string     `json:"transport"`
	Interval            string     `json:"interval"`
	LastAttempt         *time.Time `json:"last_attempt,omitempty"`
	NextDue             *time.Time `json:"next_due,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	InFlight            bool       `json:"in_flight,omitempty"`
}

// MonitorSnapshot is what the monitor is doing now.
type MonitorSnapshot struct {
	// Enabled: monitoring is switched on in the daemon settings. Running: the
	// loop is up in this process.
	Enabled  bool                  `json:"enabled"`
	Running  bool                  `json:"running"`
	Settings hosts.MonitorSettings `json:"settings"`
	Hosts    []MonitorHostState    `json:"hosts"`
}

// Snapshot reports the schedule, hosts in name order.
func (m *HostMonitor) Snapshot(ctx context.Context) MonitorSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := MonitorSnapshot{Enabled: m.settings.Enabled, Running: m.running, Settings: m.settings, Hosts: []MonitorHostState{}}
	status, _ := m.store.AllStatus(ctx)
	for _, h := range m.hostList {
		interval, ok := m.settings.IntervalFor(h)
		if !ok {
			continue
		}
		hs := MonitorHostState{Name: h.Name, Transport: h.Reach.EffectiveTransport(), Interval: interval.String(), InFlight: m.inFlight[h.Name]}
		if st, ok := status[h.Name]; ok {
			hs.LastAttempt, hs.ConsecutiveFailures = st.LastAttempt, st.ConsecutiveFailures
		}
		if d, ok := m.due[h.Name]; ok {
			dd := d
			hs.NextDue = &dd
		}
		snap.Hosts = append(snap.Hosts, hs)
	}
	sort.Slice(snap.Hosts, func(i, j int) bool { return snap.Hosts[i].Name < snap.Hosts[j].Name })
	return snap
}

var errMonitoringUnavailable = redact.Guidance("host monitoring is not available from this serving host: the daemon owns the monitor and its samples; run the command against a daemon built with it")

// AttachMonitor gives the service the monitor's store and schedule, for the
// status, samples and monitor reads and for the status on host views.
func (s *HostService) AttachMonitor(m *HostMonitor) {
	s.mon = m
}

// HostStatusView is a host's health as reported now.
type HostStatusView struct {
	Name      string `json:"name"`
	Monitored bool   `json:"monitored"`
	// Interval is how often the host is probed; empty when it is not.
	Interval string `json:"interval,omitempty"`
	// Status is the reported status: a measurement older than the stale
	// limit reads unknown, with the age and the former status in its reasons.
	Status hosts.HostStatus `json:"status"`
	Stale  bool             `json:"stale,omitempty"`
	// Thresholds are the effective values the status is derived from.
	Thresholds   hosts.Thresholds `json:"thresholds"`
	ProbeTimeout string           `json:"probe_timeout"`
}

func (s *HostService) statusView(ctx context.Context, h hosts.Host, all map[string]hosts.HostStatus) HostStatusView {
	set := s.mon.settings
	interval, monitored := set.IntervalFor(h)
	v := HostStatusView{Name: h.Name, Monitored: monitored, Thresholds: set.Thresholds, ProbeTimeout: set.ProbeTimeout.String()}
	if monitored {
		v.Interval = interval.String()
	}
	st, ok := all[h.Name]
	if !ok {
		st = hosts.HostStatus{Host: h.Name, Status: hosts.StatusUnknown}
		if monitored {
			st.Reasons = []hosts.Reason{{Code: "never_probed", Detail: "the monitor has not probed this host yet"}}
		} else {
			st.Reasons = []hosts.Reason{{Code: "not_monitored", Detail: "monitoring is off for this host or the daemon"}}
		}
		v.Status = st
		return v
	}
	if monitored {
		st, v.Stale = set.Thresholds.Effective(st, interval, s.now())
	}
	v.Status = st
	return v
}

// HostStatus returns one host's status.
func (s *HostService) HostStatus(ctx context.Context, name string) (_ HostStatusView, retErr error) {
	if s == nil || s.mon == nil {
		return HostStatusView{}, errMonitoringUnavailable
	}
	call, err := s.beginRead(ctx, OpHostStatus, map[string]any{"name": name})
	if err != nil {
		return HostStatusView{}, err
	}
	defer func() { call.finish(retErr) }()
	d, err := s.get(name)
	if err != nil {
		return HostStatusView{}, err
	}
	all, err := s.mon.store.AllStatus(ctx)
	if err != nil {
		return HostStatusView{}, err
	}
	return s.statusView(ctx, d.Host, all), nil
}

// AllHostStatus returns every host's status, in name order.
func (s *HostService) AllHostStatus(ctx context.Context) (_ []HostStatusView, retErr error) {
	if s == nil || s.mon == nil {
		return nil, errMonitoringUnavailable
	}
	call, err := s.beginRead(ctx, OpHostStatus, map[string]any{})
	if err != nil {
		return nil, err
	}
	defer func() { call.finish(retErr) }()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	all, err := s.mon.store.AllStatus(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]HostStatusView, 0, len(f.Hosts))
	for _, h := range f.Hosts {
		out = append(out, s.statusView(ctx, h, all))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	call.note(fmt.Sprintf("hosts=%d", len(out)))
	return out, nil
}

// SamplesQuery selects a host's retained samples.
type SamplesQuery struct {
	Since time.Time `json:"since,omitempty"`
	Until time.Time `json:"until,omitempty"`
	Limit int       `json:"limit,omitempty"`
	// Grain is raw (the only grain until rollups land) or empty.
	Grain string `json:"grain,omitempty"`
}

// HostSamples returns a host's samples in time order.
func (s *HostService) HostSamples(ctx context.Context, name string, q SamplesQuery) (_ []hosts.Sample, retErr error) {
	if s == nil || s.mon == nil {
		return nil, errMonitoringUnavailable
	}
	if q.Grain != "" && q.Grain != "raw" {
		return nil, &HostInvalidError{Err: redact.Guidance("grain %q returns rollups, not samples: use HostRollups (the API returns them from the same route)", q.Grain)}
	}
	call, err := s.beginRead(ctx, OpHostSamples, map[string]any{"name": name, "query": q})
	if err != nil {
		return nil, err
	}
	defer func() { call.finish(retErr) }()
	if _, err := s.get(name); err != nil {
		return nil, err
	}
	out, err := s.mon.store.Samples(ctx, name, q.Since, q.Until, q.Limit)
	call.note(fmt.Sprintf("samples=%d", len(out)))
	return out, err
}

// HostRollups returns a host's rollups of grain (5m, 1h or 1d) in time order.
func (s *HostService) HostRollups(ctx context.Context, name, grain string, q SamplesQuery) (_ []hosts.Rollup, retErr error) {
	if s == nil || s.mon == nil {
		return nil, errMonitoringUnavailable
	}
	call, err := s.beginRead(ctx, OpHostSamples, map[string]any{"name": name, "grain": grain, "query": q})
	if err != nil {
		return nil, err
	}
	defer func() { call.finish(retErr) }()
	if _, err := s.get(name); err != nil {
		return nil, err
	}
	out, err := s.mon.store.Rollups(ctx, name, grain, q.Since, q.Until, q.Limit)
	if err != nil {
		return nil, &HostInvalidError{Err: redact.Guidance("%s", err.Error())}
	}
	call.note(fmt.Sprintf("rollups=%d grain=%s", len(out), grain))
	return out, nil
}

// MonitorState reports the monitor's schedule.
func (s *HostService) MonitorState(ctx context.Context) (_ MonitorSnapshot, retErr error) {
	if s == nil || s.mon == nil {
		return MonitorSnapshot{}, errMonitoringUnavailable
	}
	call, err := s.beginRead(ctx, OpHostMonitor, map[string]any{})
	if err != nil {
		return MonitorSnapshot{}, err
	}
	defer func() { call.finish(retErr) }()
	if s.mon.hostList == nil {
		if f, err := s.load(); err == nil {
			s.mon.mu.Lock()
			s.mon.hostList = f.Hosts
			s.mon.mu.Unlock()
		}
	}
	return s.mon.Snapshot(ctx), nil
}
