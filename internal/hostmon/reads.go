package hostmon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// The read half of main's cerbapi HostService (host_monitor.go, host_events.go): a host's status,
// its samples and rollups, the monitor's schedule and the host events, over the Telemetry store.
// The wire types are main's.

// ErrNotFound marks a host hosts.yaml does not declare.
var ErrNotFound = errors.New("host not found")

// ErrInvalid marks a bad query (a time, a grain).
var ErrInvalid = errors.New("invalid host query")

// HostStatusView is a host's health as reported now.
type HostStatusView struct {
	Name      string `json:"name"`
	Monitored bool   `json:"monitored"`
	// Interval is how often the host is probed; empty when it is not.
	Interval string `json:"interval,omitempty"`
	// Status is the reported status: a measurement older than the stale limit reads unknown,
	// with the age and the former status in its reasons.
	Status hosts.HostStatus `json:"status"`
	Stale  bool             `json:"stale,omitempty"`
	// Thresholds are the effective values the status is derived from.
	Thresholds   hosts.Thresholds `json:"thresholds"`
	ProbeTimeout string           `json:"probe_timeout"`
}

// SamplesQuery selects a host's retained samples.
type SamplesQuery struct {
	Since time.Time `json:"since,omitempty"`
	Until time.Time `json:"until,omitempty"`
	Limit int       `json:"limit,omitempty"`
	// Grain is raw (or empty), 5m, 1h or 1d.
	Grain string `json:"grain,omitempty"`
}

// EventsQuery selects host events.
type EventsQuery struct {
	AfterID int64  `json:"after_id,omitempty"`
	Host    string `json:"host,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// EventGap says events after AfterID were pruned before the caller read them.
type EventGap struct {
	AfterID  int64 `json:"after_id"`
	OldestID int64 `json:"oldest_id"`
}

// EventsResult is a page of host events.
type EventsResult struct {
	Events []hosts.Event `json:"events"`
	LastID int64         `json:"last_id"`
	Gap    *EventGap     `json:"gap,omitempty"`
}

// Reads answers the host reads. Monitor is the process's monitor, running or not: its snapshot
// is the schedule. Now defaults to time.Now.
type Reads struct {
	Prober   *Prober
	Store    *hosts.SampleStore
	Monitor  *Monitor
	Settings hosts.MonitorSettings
	Now      func() time.Time
}

func (r *Reads) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// declared is the host named name, or ErrNotFound.
func (r *Reads) declared(name string) (hosts.Host, error) {
	f, err := r.Prober.Load()
	if err != nil {
		return hosts.Host{}, err
	}
	for _, h := range f.Hosts {
		if h.Name == name {
			return h, nil
		}
	}
	return hosts.Host{}, fmt.Errorf("%w: %w", ErrNotFound,
		redact.Guidance("host %q is not declared; see `cerberus host list` (hosts are declared in hosts.yaml)", name))
}

func (r *Reads) statusView(h hosts.Host, all map[string]hosts.HostStatus) HostStatusView {
	set := r.Settings
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
		st, v.Stale = set.Thresholds.Effective(st, interval, r.now())
	}
	v.Status = st
	return v
}

// Status is one host's status.
func (r *Reads) Status(ctx context.Context, name string) (HostStatusView, error) {
	h, err := r.declared(name)
	if err != nil {
		return HostStatusView{}, err
	}
	all, err := r.Store.AllStatus(ctx)
	if err != nil {
		return HostStatusView{}, err
	}
	return r.statusView(h, all), nil
}

// AllStatus is every declared host's status, in name order.
func (r *Reads) AllStatus(ctx context.Context) ([]HostStatusView, error) {
	f, err := r.Prober.Load()
	if err != nil {
		return nil, err
	}
	all, err := r.Store.AllStatus(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]HostStatusView, 0, len(f.Hosts))
	for _, h := range f.Hosts {
		out = append(out, r.statusView(h, all))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// HostStatus is the effective status a host view carries, nil when the monitor has none.
func (r *Reads) HostStatus(ctx context.Context, h hosts.Host) *hosts.HostStatus {
	st, ok, err := r.Store.Status(ctx, h.Name)
	if err != nil || !ok {
		return nil
	}
	if interval, monitored := r.Settings.IntervalFor(h); monitored {
		st, _ = r.Settings.Thresholds.Effective(st, interval, r.now())
	}
	return &st
}

// Samples is a host's raw samples in time order.
func (r *Reads) Samples(ctx context.Context, name string, q SamplesQuery) ([]hosts.Sample, error) {
	if _, err := r.declared(name); err != nil {
		return nil, err
	}
	return r.Store.Samples(ctx, name, q.Since, q.Until, q.Limit)
}

// Rollups is a host's rollups of q.Grain in time order.
func (r *Reads) Rollups(ctx context.Context, name string, q SamplesQuery) ([]hosts.Rollup, error) {
	if _, err := r.declared(name); err != nil {
		return nil, err
	}
	out, err := r.Store.Rollups(ctx, name, q.Grain, q.Since, q.Until, q.Limit)
	if err != nil && out == nil && q.Grain != "" {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return out, err
}

// Events is a page of host events, with the gap when the caller's place was pruned.
func (r *Reads) Events(ctx context.Context, q EventsQuery) (EventsResult, error) {
	evs, err := r.Store.Events(ctx, hosts.EventQuery{AfterID: q.AfterID, Host: q.Host, Kind: q.Kind, Limit: q.Limit})
	if err != nil {
		return EventsResult{}, err
	}
	if evs == nil {
		evs = []hosts.Event{}
	}
	res := EventsResult{Events: evs, LastID: q.AfterID}
	if n := len(evs); n > 0 {
		res.LastID = evs[n-1].ID
	}
	oldest, _, err := r.Store.EventBounds(ctx)
	if err != nil {
		return res, err
	}
	if q.AfterID > 0 && oldest > 0 && q.AfterID+1 < oldest {
		res.Gap = &EventGap{AfterID: q.AfterID, OldestID: oldest}
	}
	return res, nil
}

// MonitorState is the monitor's schedule.
func (r *Reads) MonitorState(ctx context.Context) MonitorSnapshot {
	return r.Monitor.Snapshot(ctx)
}

// ParseSamplesTime reads a samples bound: RFC 3339, or a duration back from now (main's).
func ParseSamplesTime(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("%w: %w", ErrInvalid, redact.Guidance("%q is not a time: use RFC 3339 or a duration such as 6h", s))
}
