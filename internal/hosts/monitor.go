package hosts

import (
	"fmt"
	"math"
	"time"
)

// Status values of a monitored host.
const (
	StatusOnline   = "online"
	StatusDegraded = "degraded"
	StatusOffline  = "offline"
	StatusUnknown  = "unknown"
)

// Monitor is the optional per-host monitoring override in hosts.yaml.
type Monitor struct {
	// Enabled is a pointer so a host can switch monitoring off (false) or
	// leave it to the daemon default (absent).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Interval is a Go duration, such as 30s or 5m.
	Interval string `yaml:"interval,omitempty" json:"interval,omitempty"`
}

// Thresholds are the readable facts a status is derived from. They are
// settings, not scores: nothing here ranks or compares hosts.
type Thresholds struct {
	DiskFreePercent        float64 `json:"disk_free_percent"`        // disk_low when free is below this percent...
	DiskFreeBytes          int64   `json:"disk_free_bytes"`          // ...or below this many bytes
	MemoryAvailablePercent float64 `json:"memory_available_percent"` // memory_low
	LoadFactor             float64 `json:"load_factor"`              // load_high when load1 > factor x cpu...
	LoadSamples            int     `json:"load_samples"`             // ...for this many consecutive samples
	SlowProbeFraction      float64 `json:"slow_probe_fraction"`      // probe_slow when probe_ms > fraction x timeout...
	SlowProbeSamples       int     `json:"slow_probe_samples"`       // ...for this many samples
	OfflineAfter           int     `json:"offline_after"`            // consecutive failed probes before offline
	DegradeConfirm         int     `json:"degrade_confirm"`          // consecutive consistent results to enter or leave degraded
	StaleIntervals         int     `json:"stale_intervals"`          // a measurement older than this many intervals is stale
}

// MonitorSettings are the daemon's monitoring settings, resolved.
type MonitorSettings struct {
	Enabled         bool          `json:"enabled"`
	OutpostInterval time.Duration `json:"outpost_interval"`
	SSHInterval     time.Duration `json:"ssh_interval"`
	MinInterval     time.Duration `json:"min_interval"`
	// Jitter is the fraction of an interval randomized either way (0.1 = ±10 %).
	Jitter         float64       `json:"jitter"`
	MaxBackoff     time.Duration `json:"max_backoff"`
	Concurrency    int           `json:"concurrency"`
	SSHConcurrency int           `json:"ssh_concurrency"`
	ProbeTimeout   time.Duration `json:"probe_timeout"`
	// SampleRetention is how long raw samples are kept; Rollup5mRetention and
	// Rollup1hRetention how long those rollups are. Daily rollups are kept.
	SampleRetention   time.Duration `json:"sample_retention"`
	Rollup5mRetention time.Duration `json:"rollup_5m_retention"`
	Rollup1hRetention time.Duration `json:"rollup_1h_retention"`
	Thresholds        Thresholds    `json:"thresholds"`
}

// DefaultMonitorSettings are the documented defaults. Monitoring is off until
// the operator turns it on.
func DefaultMonitorSettings() MonitorSettings {
	return MonitorSettings{
		OutpostInterval: 60 * time.Second, SSHInterval: 5 * time.Minute, MinInterval: 15 * time.Second,
		Jitter: 0.10, MaxBackoff: 15 * time.Minute, Concurrency: 4, SSHConcurrency: 2, ProbeTimeout: 45 * time.Second,
		SampleRetention: 7 * 24 * time.Hour, Rollup5mRetention: 30 * 24 * time.Hour, Rollup1hRetention: 400 * 24 * time.Hour,
		Thresholds: Thresholds{
			DiskFreePercent: 10, DiskFreeBytes: 5 << 30, MemoryAvailablePercent: 10,
			LoadFactor: 2, LoadSamples: 3, SlowProbeFraction: 0.5, SlowProbeSamples: 3,
			OfflineAfter: 3, DegradeConfirm: 2, StaleIntervals: 3,
		},
	}
}

// Validate rejects settings that would hammer a host or never settle.
func (m MonitorSettings) Validate() error {
	switch {
	case m.MinInterval < time.Second:
		return fmt.Errorf("host_monitor min_interval %s is under 1s", m.MinInterval)
	case m.OutpostInterval < m.MinInterval || m.SSHInterval < m.MinInterval:
		return fmt.Errorf("host_monitor intervals must be at least min_interval (%s)", m.MinInterval)
	case m.Jitter < 0 || m.Jitter > 0.5:
		return fmt.Errorf("host_monitor jitter must be between 0 and 0.5")
	case m.Concurrency < 1 || m.SSHConcurrency < 1:
		return fmt.Errorf("host_monitor concurrency must be at least 1")
	case m.ProbeTimeout < time.Second:
		return fmt.Errorf("host_monitor probe_timeout is under 1s")
	case m.SampleRetention < time.Hour || m.Rollup5mRetention < m.SampleRetention || m.Rollup1hRetention < m.Rollup5mRetention:
		return fmt.Errorf("host_monitor retention must be at least 1h for raw samples and not shrink from raw to 5m to 1h rollups")
	case m.MaxBackoff < m.MinInterval:
		return fmt.Errorf("host_monitor max_backoff is under min_interval")
	case m.Thresholds.OfflineAfter < 1 || m.Thresholds.DegradeConfirm < 1 || m.Thresholds.StaleIntervals < 1 || m.Thresholds.LoadSamples < 1 || m.Thresholds.SlowProbeSamples < 1:
		return fmt.Errorf("host_monitor thresholds that count samples or probes must be at least 1")
	}
	return nil
}

// Retention is the tiered retention the janitor applies.
func (m MonitorSettings) Retention() Retention {
	return Retention{Raw: m.SampleRetention, Rollup5m: m.Rollup5mRetention, Rollup1h: m.Rollup1hRetention}
}

// IntervalFor is how often h is probed, and whether it is monitored at all: a
// host with no usable transport, or with monitoring switched off, is not.
func (m MonitorSettings) IntervalFor(h Host) (time.Duration, bool) {
	if h.Monitor != nil && h.Monitor.Enabled != nil && !*h.Monitor.Enabled {
		return 0, false
	}
	var d time.Duration
	switch h.Reach.EffectiveTransport() {
	case "outpost":
		d = m.OutpostInterval
	case "ssh":
		d = m.SSHInterval
	default:
		return 0, false
	}
	if h.Monitor != nil && h.Monitor.Interval != "" {
		if own, err := time.ParseDuration(h.Monitor.Interval); err == nil && own > 0 {
			d = own
		}
	}
	if d < m.MinInterval {
		d = m.MinInterval
	}
	return d, true
}

// NextDelay is the wait before the next probe: the interval, doubled for each
// consecutive failure up to maxBackoff, with ±jitter. rnd returns [0,1).
func NextDelay(interval time.Duration, failures int, maxBackoff time.Duration, jitter float64, rnd func() float64) time.Duration {
	d := float64(interval)
	if failures > 0 {
		d *= math.Pow(2, float64(failures))
		if d > float64(maxBackoff) {
			d = float64(maxBackoff)
		}
		if d < float64(interval) {
			d = float64(interval)
		}
	}
	return time.Duration(d * (1 + jitter*(2*rnd()-1)))
}

// StartDelay staggers the first probe of a host over its interval so a daemon
// restart does not probe every host at once.
func StartDelay(interval time.Duration, rnd func() float64) time.Duration {
	return time.Duration(float64(interval) * rnd())
}

// Reason is one stated cause of a status, in plain words.
type Reason struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// HostStatus is a host's derived health.
type HostStatus struct {
	Host    string   `json:"host"`
	Status  string   `json:"status"`
	Reasons []Reason `json:"reasons,omitempty"`
	// Since is when Status last changed.
	Since       time.Time  `json:"since"`
	LastOK      *time.Time `json:"last_ok,omitempty"`
	LastAttempt *time.Time `json:"last_attempt,omitempty"`
	NextDue     *time.Time `json:"next_due,omitempty"`
	// ConsecutiveFailures counts failed probes since the last success.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// Candidate and CandidateCount are the flap damping state: a status the
	// last CandidateCount results agree on, not yet adopted.
	Candidate      string `json:"candidate,omitempty"`
	CandidateCount int    `json:"candidate_count,omitempty"`
}

// Sample is one successful measurement.
type Sample struct {
	Host            string    `json:"host"`
	At              time.Time `json:"ts"`
	Load1           *float64  `json:"load1,omitempty"`
	Load5           *float64  `json:"load5,omitempty"`
	Load15          *float64  `json:"load15,omitempty"`
	CPU             int       `json:"cpu,omitempty"`
	MemoryTotal     int64     `json:"memory_total,omitempty"`
	MemoryAvailable int64     `json:"memory_available,omitempty"`
	DiskTotal       int64     `json:"disk_total,omitempty"`
	DiskFree        int64     `json:"disk_free,omitempty"`
	UptimeSeconds   int64     `json:"uptime_seconds,omitempty"`
	ProbeMS         int64     `json:"probe_ms,omitempty"`
}

// SampleFrom builds the sample a successful probe yields.
func SampleFrom(host string, at time.Time, p *Probed, probeMS int64) Sample {
	return Sample{Host: host, At: at.UTC().Truncate(time.Second), Load1: p.Load1, Load5: p.Load5, Load15: p.Load15, CPU: p.CPU,
		MemoryTotal: p.MemoryTotal, MemoryAvailable: p.MemoryAvailable, DiskTotal: p.DiskTotal, DiskFree: p.DiskFree,
		UptimeSeconds: p.UptimeSeconds, ProbeMS: probeMS}
}

// Observation is what one probe attempt found.
type Observation struct {
	At time.Time
	OK bool
	// Partial: the host answered but only some facts are known.
	Partial bool
	Sample  *Sample
	// Err says, in plain words, why a failed attempt failed.
	Err string
}

// Reasons lists the thresholds a successful observation crosses. recent is the
// host's latest samples, newest first, and includes the observation's own.
func (t Thresholds) Reasons(o Observation, recent []Sample, timeout time.Duration) []Reason {
	var out []Reason
	s := o.Sample
	if s == nil {
		return out
	}
	if s.DiskTotal > 0 {
		pct := float64(s.DiskFree) / float64(s.DiskTotal) * 100
		if pct < t.DiskFreePercent || s.DiskFree < t.DiskFreeBytes {
			out = append(out, Reason{"disk_low", fmt.Sprintf("%.0f%% free (%s) of the data disk", pct, humanSize(s.DiskFree))})
		}
	}
	if s.MemoryTotal > 0 {
		if pct := float64(s.MemoryAvailable) / float64(s.MemoryTotal) * 100; pct < t.MemoryAvailablePercent {
			out = append(out, Reason{"memory_low", fmt.Sprintf("%.0f%% of memory available (%s)", pct, humanSize(s.MemoryAvailable))})
		}
	}
	if last := lastN(recent, t.LoadSamples); len(last) == t.LoadSamples {
		high := true
		for _, r := range last {
			if r.Load1 == nil || r.CPU <= 0 || *r.Load1 <= t.LoadFactor*float64(r.CPU) {
				high = false
				break
			}
		}
		if high {
			out = append(out, Reason{"load_high", fmt.Sprintf("load1 %.2f on %d cores for %d samples running", *s.Load1, s.CPU, t.LoadSamples)})
		}
	}
	if last := lastN(recent, t.SlowProbeSamples); len(last) == t.SlowProbeSamples && timeout > 0 {
		slow := true
		for _, r := range last {
			if float64(r.ProbeMS) <= t.SlowProbeFraction*float64(timeout.Milliseconds()) {
				slow = false
				break
			}
		}
		if slow {
			out = append(out, Reason{"probe_slow", fmt.Sprintf("probes take %dms, over %.0f%% of the %s timeout, %d in a row", s.ProbeMS, t.SlowProbeFraction*100, timeout, t.SlowProbeSamples)})
		}
	}
	if o.Partial {
		out = append(out, Reason{"partial", "the host answered but reported only some facts"})
	}
	return out
}

func lastN(recent []Sample, n int) []Sample {
	if len(recent) < n {
		return recent
	}
	return recent[:n]
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Advance applies one observation to a host's status. It returns the new
// status and whether Status changed.
//
// A failure counts toward offline (OfflineAfter in a row). A success clears
// the failures and moves to online or degraded: at once from offline or
// unknown, and after DegradeConfirm consistent results when moving between
// online and degraded (flap damping).
func (t Thresholds) Advance(prev HostStatus, o Observation, recent []Sample, timeout time.Duration) (HostStatus, bool) {
	next := prev
	at := o.At
	next.LastAttempt = &at
	if prev.Status == "" {
		next.Status, next.Since = StatusUnknown, at
	}
	if !o.OK {
		next.ConsecutiveFailures++
		next.Candidate, next.CandidateCount = "", 0
		if next.ConsecutiveFailures >= t.OfflineAfter && next.Status != StatusOffline {
			next.Status, next.Since = StatusOffline, at
			next.Reasons = []Reason{{"unreachable", fmt.Sprintf("%d probes in a row failed: %s", next.ConsecutiveFailures, o.Err)}}
			return next, true
		}
		if next.Status == StatusOffline {
			next.Reasons = []Reason{{"unreachable", fmt.Sprintf("%d probes in a row failed: %s", next.ConsecutiveFailures, o.Err)}}
		} else {
			next.Reasons = append([]Reason(nil), prev.Reasons...)
			next.Reasons = withoutCode(next.Reasons, "probe_failed")
			next.Reasons = append(next.Reasons, Reason{"probe_failed", fmt.Sprintf("last probe failed (%d of %d before offline): %s", next.ConsecutiveFailures, t.OfflineAfter, o.Err)})
		}
		return next, false
	}

	next.ConsecutiveFailures = 0
	ok := at
	next.LastOK = &ok
	reasons := t.Reasons(o, recent, timeout)
	target := StatusOnline
	if len(reasons) > 0 {
		target = StatusDegraded
	}
	switch {
	case next.Status == target:
		next.Candidate, next.CandidateCount = "", 0
		next.Reasons = reasons
		return next, false
	case next.Status == StatusOffline || next.Status == StatusUnknown:
		next.Status, next.Since, next.Reasons = target, at, reasons
		next.Candidate, next.CandidateCount = "", 0
		return next, true
	}
	// online <-> degraded: needs DegradeConfirm consistent results.
	if next.Candidate == target {
		next.CandidateCount++
	} else {
		next.Candidate, next.CandidateCount = target, 1
	}
	next.Reasons = reasons
	if next.CandidateCount >= t.DegradeConfirm {
		next.Status, next.Since = target, at
		next.Candidate, next.CandidateCount = "", 0
		return next, true
	}
	if next.Status == StatusOnline {
		next.Reasons = nil // not adopted yet: online stays reason-free
	}
	return next, false
}

func withoutCode(rs []Reason, code string) []Reason {
	out := rs[:0:0]
	for _, r := range rs {
		if r.Code != code {
			out = append(out, r)
		}
	}
	return out
}

// Effective is the status as reported at time now: a measurement older than
// StaleIntervals intervals is stale, so the host reads unknown with the age
// and the last status named, never silently kept as online.
func (t Thresholds) Effective(s HostStatus, interval time.Duration, now time.Time) (HostStatus, bool) {
	if s.Status != StatusOnline && s.Status != StatusDegraded {
		return s, false
	}
	if s.LastOK == nil || interval <= 0 {
		return s, false
	}
	age := now.Sub(*s.LastOK)
	if age <= time.Duration(t.StaleIntervals)*interval {
		return s, false
	}
	out := s
	out.Status = StatusUnknown
	out.Reasons = []Reason{{"stale", fmt.Sprintf("last good measurement is %s old (over %d intervals of %s); it was %s", age.Round(time.Second), t.StaleIntervals, interval, s.Status)}}
	return out, true
}
