package cerbapi

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

const OpFleetSnapshot = "fleet_snapshot"

// FleetSnapshotQuery bounds a page of known hosts; no remote probes are run.
type FleetSnapshotQuery struct {
	Offset int `json:"offset,omitempty"`
	Limit  int `json:"limit,omitempty"`
}

// FleetSnapshot is the public fleet health DTO. It excludes host connection
// settings, labels, raw probe output, units and any agent/session information.
type FleetSnapshot struct {
	Schema      string              `json:"schema"`
	GeneratedAt time.Time           `json:"generated_at"`
	WindowStart time.Time           `json:"window_start"`
	StepSeconds int                 `json:"step_seconds"`
	TotalHosts  int                 `json:"total_hosts"`
	NextOffset  *int                `json:"next_offset"`
	Hosts       []FleetSnapshotHost `json:"hosts"`
}

type FleetSnapshotHost struct {
	Name      string        `json:"name"`
	Roles     []string      `json:"roles"`
	Reachable *bool         `json:"reachable"`
	LastSeen  *time.Time    `json:"last_seen"`
	Stale     bool          `json:"stale"`
	Latest    *FleetMetrics `json:"latest"`
	// MetricsGap explains why a reachable host's CPU and per-mount disk are
	// null; null when there is nothing to explain.
	MetricsGap *FleetMetricsGap `json:"metrics_gap"`
	Series     []FleetPoint     `json:"series"`
}

// FleetMetricsGap codes.
const (
	MetricsGapProbeOutdated = "probe_outdated"
	// minProbeVersion is the first probe script schema that reports CPU percent
	// and per-mount disk.
	minProbeVersion = 2
)

// FleetMetricsGap is an operator-facing reason for missing metrics.
type FleetMetricsGap struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// FleetMetrics uses null for unmeasured values; CPU percent is never load or
// core count. Memory used is total minus available; disk used is df's used.
type FleetMetrics struct {
	TS          time.Time           `json:"ts"`
	CPUPercent  *float64            `json:"cpu_percent"`
	MemoryUsed  *int64              `json:"memory_used"`
	MemoryTotal *int64              `json:"memory_total"`
	Disks       []hosts.MountSample `json:"disks"`
	Load1       *float64            `json:"load1"`
	Load5       *float64            `json:"load5"`
	Load15      *float64            `json:"load15"`
}

// FleetPoint represents a bucket whose TS is its start. SampleAt is the measurement time, or null
// for an empty bucket. Selection is last-in-bucket, not an average.
type FleetPoint struct {
	FleetMetrics
	SampleAt *time.Time `json:"sample_at"`
}

func finiteMetric(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 {
		return nil
	}
	return v
}

func fleetMetrics(x hosts.Sample) FleetMetrics {
	m := FleetMetrics{TS: x.At, CPUPercent: finiteMetric(x.CPUPercent), Disks: []hosts.MountSample{}, Load1: finiteMetric(x.Load1), Load5: finiteMetric(x.Load5), Load15: finiteMetric(x.Load15)}
	if m.CPUPercent != nil && *m.CPUPercent > 100 {
		m.CPUPercent = nil
	}
	if x.MemoryTotal > 0 {
		total := x.MemoryTotal
		m.MemoryTotal = &total
		if !x.MemoryAvailableUnknown && x.MemoryAvailable >= 0 && x.MemoryAvailable <= x.MemoryTotal {
			used := x.MemoryTotal - x.MemoryAvailable
			m.MemoryUsed = &used
		}
	}
	seen := map[string]bool{}
	for _, d := range x.Mounts {
		if hosts.IsMainMount(d.Mount) && !seen[d.Mount] && d.Total > 0 && d.Used >= 0 && d.Used <= d.Total {
			m.Disks = append(m.Disks, d)
			seen[d.Mount] = true
		}
	}
	sort.Slice(m.Disks, func(i, j int) bool { return m.Disks[i].Mount < m.Disks[j].Mount })
	return m
}

// FleetSnapshot reads a bounded page from the daemon-owned monitor store.
func (s *HostService) FleetSnapshot(ctx context.Context, q FleetSnapshotQuery) (_ FleetSnapshot, retErr error) {
	if s == nil || s.mon == nil {
		return FleetSnapshot{}, errMonitoringUnavailable
	}
	if q.Offset < 0 || q.Limit < 0 || q.Limit > 100 {
		return FleetSnapshot{}, &HostInvalidError{Err: redact.Guidance("offset must be nonnegative and limit must be 1..100 (or omitted)")}
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	call, err := s.beginRead(ctx, OpFleetSnapshot, map[string]any{"offset": q.Offset, "limit": q.Limit})
	if err != nil {
		return FleetSnapshot{}, err
	}
	defer func() { call.finish(retErr) }()
	f, err := s.load()
	if err != nil {
		return FleetSnapshot{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	out := FleetSnapshot{Schema: "cerberus.fleet.snapshot.v1", GeneratedAt: now, WindowStart: now.Add(-time.Hour), StepSeconds: 300, TotalHosts: len(f.Hosts), Hosts: []FleetSnapshotHost{}}
	sort.Slice(f.Hosts, func(i, j int) bool { return f.Hosts[i].Name < f.Hosts[j].Name })
	if q.Offset >= len(f.Hosts) {
		return out, nil
	}
	end := q.Offset + q.Limit
	if end > len(f.Hosts) {
		end = len(f.Hosts)
	}
	if end < len(f.Hosts) {
		out.NextOffset = &end
	}
	for _, h := range f.Hosts[q.Offset:end] {
		last, samples, err := s.mon.store.SnapshotSamples(ctx, h.Name, now)
		if err != nil {
			return FleetSnapshot{}, err
		}
		st, ok, err := s.mon.store.Status(ctx, h.Name)
		if err != nil {
			return FleetSnapshot{}, err
		}
		v := FleetSnapshotHost{Name: h.Name, Roles: []string{}, Stale: true, Series: make([]FleetPoint, 12)}
		if h.Role != "" {
			v.Roles = append(v.Roles, h.Role)
		}
		if ok {
			v.LastSeen = st.LastOK
		}
		if last != nil {
			m := fleetMetrics(*last)
			v.Latest = &m
			if v.LastSeen == nil || last.At.After(*v.LastSeen) {
				v.LastSeen = &last.At
			}
		}
		interval, monitored := s.mon.settings.IntervalFor(h)
		// Disabled monitoring supplies historical metrics but no current reachability.
		if s.mon.settings.Enabled && monitored && ok && st.LastAttempt != nil && !st.LastAttempt.After(now) && now.Sub(*st.LastAttempt) <= 3*interval {
			v.Stale = false
			reachable := st.ConsecutiveFailures == 0 && st.LastOK != nil
			v.Reachable = &reachable
			if reachable && last != nil && last.ProbeVersion < minProbeVersion {
				v.MetricsGap = &FleetMetricsGap{Code: MetricsGapProbeOutdated, Detail: "this host's cerberus-outpost plugin predates CPU% and per-mount disk metrics (probe_version 2); restage cerberus-outpost on the host to populate them"}
			}
		}
		for i := range v.Series {
			v.Series[i].FleetMetrics = FleetMetrics{TS: out.WindowStart.Add(time.Duration(i) * 5 * time.Minute), Disks: []hosts.MountSample{}}
		}
		for _, x := range samples {
			i := int(x.At.Sub(out.WindowStart) / (5 * time.Minute))
			if i >= 0 && i < 12 {
				at := x.At
				m := fleetMetrics(x)
				m.TS = v.Series[i].TS
				v.Series[i] = FleetPoint{FleetMetrics: m, SampleAt: &at}
			}
		}
		out.Hosts = append(out.Hosts, v)
	}
	return out, nil
}
