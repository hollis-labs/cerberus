package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// CapacityQuery asks which declared hosts satisfy a set of constraints. It is
// a filter over facts. It carries no weights, no preference and no count to
// pick: Cerberus returns every host that satisfies it, in name order, and the
// consumer (Tether's provider adapter, ADR 0004) decides where work goes.
type CapacityQuery struct {
	// MinMemoryAvailable, MinDiskFree are measured minimums, in bytes.
	MinMemoryAvailable hosts.Size
	MinDiskFree        hosts.Size
	// MinCPU is the minimum probed logical core count.
	MinCPU int
	// Role and Provider are exact matches on the declared record.
	Role     string
	Provider string
	// Labels must all be present with exactly these values.
	Labels map[string]string
	// RequireTools are probed tools (docker, tmux, tailscale, caddy) that
	// must be installed.
	RequireTools []string
	// MaxStaleAge is the oldest probe a measured constraint is judged on.
	// Zero is the service's stale threshold.
	MaxStaleAge time.Duration
	// IncludeOffline judges an offline host on its last measurement, which
	// is shown with its age. Without it an offline host is excluded.
	IncludeOffline bool
	// Explain also lists the excluded hosts with the reason for each.
	Explain bool

	// Hardware constraints (Hosts F1). Each tests a recorded fact; a host
	// whose fact is not recorded is unknown for it, never guessed.
	// MinMemoryTotal is installed memory (probed when fresh, else recorded).
	MinMemoryTotal hosts.Size
	// MinMemoryUpgrade is the room to add memory: the recorded memory ceiling
	// minus the installed total.
	MinMemoryUpgrade hosts.Size
	// FreeMemorySlots is the number of empty memory slots required.
	FreeMemorySlots int
	// FreeStorageSlots are free slots required by kind.
	FreeStorageSlots []SlotNeed
	// DiskTypes: at least one recorded disk must be of one of these types.
	DiskTypes []string
	// MinDiskSize: at least one recorded disk at least this large.
	MinDiskSize hosts.Size
	// StatusIn: the monitor's status must be one of these.
	StatusIn []string
	// Recent is headroom held over a window of samples.
	Recent *RecentNeed
}

// SlotNeed asks for at least Min free storage slots of Kind.
type SlotNeed struct {
	Kind string `json:"kind"`
	Min  int    `json:"min"`
}

// RecentNeed is headroom that held throughout a window: the host passes when
// its worst sample in the window (highest load per core, lowest memory
// available, lowest free disk) still meets every limit given.
type RecentNeed struct {
	Window             time.Duration
	MaxLoadPerCore     float64
	MinMemoryAvailable hosts.Size
	MinDiskFree        hosts.Size
}

type recentWire struct {
	Window             string     `json:"window,omitempty"`
	MaxLoadPerCore     float64    `json:"max_load_per_core,omitempty"`
	MinMemoryAvailable hosts.Size `json:"min_memory_available,omitempty"`
	MinDiskFree        hosts.Size `json:"min_disk_free,omitempty"`
}

// capacityQueryWire is CapacityQuery on the wire: sizes read as bytes or
// "16GiB", the age as a duration string.
type capacityQueryWire struct {
	MinMemoryAvailable hosts.Size        `json:"min_memory_available,omitempty"`
	MinCPU             int               `json:"min_cpu,omitempty"`
	MinDiskFree        hosts.Size        `json:"min_disk_free,omitempty"`
	Role               string            `json:"role,omitempty"`
	Provider           string            `json:"provider,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	RequireTools       []string          `json:"require_tools,omitempty"`
	MaxStaleAge        string            `json:"max_stale_age,omitempty"`
	IncludeOffline     bool              `json:"include_offline,omitempty"`
	Explain            bool              `json:"explain,omitempty"`

	MinMemoryTotal   hosts.Size  `json:"min_memory_total,omitempty"`
	MinMemoryUpgrade hosts.Size  `json:"min_memory_upgrade,omitempty"`
	FreeMemorySlots  int         `json:"free_memory_slots,omitempty"`
	FreeStorageSlots []SlotNeed  `json:"free_storage_slots,omitempty"`
	DiskTypes        []string    `json:"disk_types,omitempty"`
	MinDiskSize      hosts.Size  `json:"min_disk_size,omitempty"`
	StatusIn         []string    `json:"status_in,omitempty"`
	Recent           *recentWire `json:"recent,omitempty"`
}

// MarshalJSON writes the wire form.
func (q CapacityQuery) MarshalJSON() ([]byte, error) {
	w := capacityQueryWire{MinMemoryAvailable: q.MinMemoryAvailable, MinCPU: q.MinCPU, MinDiskFree: q.MinDiskFree,
		Role: q.Role, Provider: q.Provider, Labels: q.Labels, RequireTools: q.RequireTools,
		IncludeOffline: q.IncludeOffline, Explain: q.Explain,
		MinMemoryTotal: q.MinMemoryTotal, MinMemoryUpgrade: q.MinMemoryUpgrade, FreeMemorySlots: q.FreeMemorySlots,
		FreeStorageSlots: q.FreeStorageSlots, DiskTypes: q.DiskTypes, MinDiskSize: q.MinDiskSize, StatusIn: q.StatusIn}
	if q.MaxStaleAge > 0 {
		w.MaxStaleAge = q.MaxStaleAge.String()
	}
	if r := q.Recent; r != nil {
		w.Recent = &recentWire{Window: r.Window.String(), MaxLoadPerCore: r.MaxLoadPerCore, MinMemoryAvailable: r.MinMemoryAvailable, MinDiskFree: r.MinDiskFree}
	}
	return json.Marshal(w)
}

// UnmarshalJSON reads the wire form and refuses unknown fields, so a
// constraint a daemon does not know is an error, never silently dropped (a
// dropped constraint would return hosts that do not satisfy the query).
func (q *CapacityQuery) UnmarshalJSON(data []byte) error {
	var w capacityQueryWire
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return &HostInvalidError{Err: redact.Guidance("the capacity query must be a JSON object of its documented fields (min_memory_available, min_cpu, min_disk_free, role, provider, labels, require_tools, max_stale_age, include_offline, explain, min_memory_total, min_memory_upgrade, free_memory_slots, free_storage_slots, disk_types, min_disk_size, status_in, recent)")}
	}
	out := CapacityQuery{MinMemoryAvailable: w.MinMemoryAvailable, MinCPU: w.MinCPU, MinDiskFree: w.MinDiskFree,
		Role: w.Role, Provider: w.Provider, Labels: w.Labels, RequireTools: w.RequireTools,
		IncludeOffline: w.IncludeOffline, Explain: w.Explain,
		MinMemoryTotal: w.MinMemoryTotal, MinMemoryUpgrade: w.MinMemoryUpgrade, FreeMemorySlots: w.FreeMemorySlots,
		FreeStorageSlots: w.FreeStorageSlots, DiskTypes: w.DiskTypes, MinDiskSize: w.MinDiskSize, StatusIn: w.StatusIn}
	if r := w.Recent; r != nil {
		need := &RecentNeed{MaxLoadPerCore: r.MaxLoadPerCore, MinMemoryAvailable: r.MinMemoryAvailable, MinDiskFree: r.MinDiskFree, Window: defaultRecentWindow}
		if r.Window != "" {
			d, err := time.ParseDuration(r.Window)
			if err != nil || d <= 0 {
				return &HostInvalidError{Err: redact.Guidance("recent.window must be a positive duration such as 15m")}
			}
			need.Window = d
		}
		out.Recent = need
	}
	if w.MaxStaleAge != "" {
		d, err := time.ParseDuration(w.MaxStaleAge)
		if err != nil {
			return &HostInvalidError{Err: redact.Guidance("max_stale_age must be a duration such as 10m or 2h")}
		}
		out.MaxStaleAge = d
	}
	*q = out
	return nil
}

// HostVerdict says why a host was not matched. For an unknown host it names
// what could not be judged; for an excluded one, what failed.
type HostVerdict struct {
	Name      string     `json:"name"`
	State     string     `json:"state"`
	ProbedAt  *time.Time `json:"probed_at,omitempty"`
	Reasons   []string   `json:"reasons"`
	NextStep  string     `json:"next_step,omitempty"`
	AgeSecond *int64     `json:"probe_age_seconds,omitempty"`
}

// CapacityResult is the hosts that satisfy a CapacityQuery. There is no rank,
// score or preferred host in it, and Matched is in name order.
type CapacityResult struct {
	// Matched satisfy every constraint on facts measured within MaxStaleAge.
	Matched []HostView `json:"matched"`
	// Unknown could not be judged: never probed, probed too long ago, or the
	// probe did not report a fact a constraint needs. They are not guessed
	// to match or to fail.
	Unknown []HostVerdict `json:"unknown"`
	// Excluded is set only when the query asked to explain.
	Excluded []HostVerdict `json:"excluded,omitempty"`
	// MaxStaleAge is the age limit that applied, as a duration string.
	MaxStaleAge string `json:"max_stale_age"`
	// Total is how many hosts are declared.
	Total int `json:"total_hosts"`
	// Judged are the worst-case facts a matched host was judged on for a
	// recent-headroom constraint, by host name, so a consumer can decide
	// between matches itself. Facts only: nothing here orders the hosts.
	Judged map[string]RecentFacts `json:"judged,omitempty"`
}

// RecentFacts are the worst values in a window and how many samples there were.
type RecentFacts struct {
	Window             string  `json:"window"`
	Samples            int     `json:"samples"`
	Expected           int     `json:"expected_samples"`
	WorstLoadPerCore   float64 `json:"worst_load_per_core,omitempty"`
	MinMemoryAvailable int64   `json:"min_memory_available,omitempty"`
	MinDiskFree        int64   `json:"min_disk_free,omitempty"`
}

const defaultRecentWindow = 15 * time.Minute

// HostInvalidError is a request the service refuses as malformed.
type HostInvalidError struct{ Err error }

func (e *HostInvalidError) Error() string { return e.Err.Error() }
func (e *HostInvalidError) Unwrap() error { return e.Err }

// RenderRedacted renders the wrapped error: its prose stays prose.
func (e *HostInvalidError) RenderRedacted(s *redact.Scope) string { return redact.Render(s, e.Err) }

func (q CapacityQuery) validate() error {
	invalid := func(format string, args ...any) error {
		return &HostInvalidError{Err: redact.Guidance(format, args...)}
	}
	if q.MinMemoryAvailable < 0 || q.MinDiskFree < 0 || q.MinCPU < 0 {
		return invalid("capacity minimums must not be negative")
	}
	if q.MaxStaleAge < 0 {
		return invalid("max_stale_age must not be negative")
	}
	if q.MinMemoryTotal < 0 || q.MinMemoryUpgrade < 0 || q.MinDiskSize < 0 || q.FreeMemorySlots < 0 {
		return invalid("capacity minimums must not be negative")
	}
	for _, n := range q.FreeStorageSlots {
		if !slices.Contains(hosts.StorageSlotKinds, n.Kind) {
			return invalid("unknown storage slot kind %q; known: %s", n.Kind, strings.Join(hosts.StorageSlotKinds, ", "))
		}
		if n.Min < 1 {
			return invalid("free_storage_slots min for %s must be at least 1", n.Kind)
		}
	}
	for _, t := range q.DiskTypes {
		if !slices.Contains(hosts.HardwareDiskTypes, t) {
			return invalid("unknown disk type %q; known: %s", t, strings.Join(hosts.HardwareDiskTypes, ", "))
		}
	}
	for _, st := range q.StatusIn {
		if !slices.Contains([]string{hosts.StatusOnline, hosts.StatusDegraded, hosts.StatusOffline, hosts.StatusUnknown}, st) {
			return invalid("unknown status %q; known: online, degraded, offline, unknown", st)
		}
	}
	if r := q.Recent; r != nil {
		if r.Window <= 0 || r.MaxLoadPerCore < 0 || r.MinMemoryAvailable < 0 || r.MinDiskFree < 0 {
			return invalid("recent needs a positive window and non-negative limits")
		}
		if r.Window > 7*24*time.Hour {
			return invalid("recent.window is at most 7d: longer windows need the rollups, which a headroom check does not use yet")
		}
		if r.MaxLoadPerCore == 0 && r.MinMemoryAvailable == 0 && r.MinDiskFree == 0 {
			return invalid("recent needs at least one limit: max_load_per_core, min_memory_available or min_disk_free")
		}
	}
	for k := range q.Labels {
		if strings.TrimSpace(k) == "" {
			return invalid("a label constraint needs a key")
		}
	}
	for _, t := range q.RequireTools {
		known := false
		for _, k := range hosts.ProbedTools {
			known = known || k == t
		}
		if !known {
			return invalid("a probe does not report the tool %q; the tools it reports are %s", t, strings.Join(hosts.ProbedTools, ", "))
		}
	}
	return nil
}

// measured reports whether the query depends on probed facts.
func (q CapacityQuery) measured() bool {
	return q.MinMemoryAvailable > 0 || q.MinDiskFree > 0 || q.MinCPU > 0 || len(q.RequireTools) > 0
}

// Capacity returns the hosts that satisfy q, in name order, with the facts
// that were measured and how old they are. It reads recorded facts and does
// not probe: a consumer that needs fresh facts probes first. It does not rank,
// reserve or choose a host, and it never guesses at a host it has no fresh
// facts for: that host is reported as unknown, with the reason.
func (s *HostService) Capacity(ctx context.Context, q CapacityQuery) (_ CapacityResult, retErr error) {
	if s == nil {
		return CapacityResult{}, errors.New("host service is not configured")
	}
	call, err := s.beginRead(ctx, OpHostCapacity, map[string]any{"query": q})
	if err != nil {
		return CapacityResult{}, err
	}
	defer func() { call.finish(retErr) }()
	if err = q.validate(); err != nil {
		return CapacityResult{}, err
	}
	views, err := s.list(HostFilter{})
	if err != nil {
		return CapacityResult{}, err
	}
	maxAge := q.MaxStaleAge
	if maxAge <= 0 {
		maxAge = s.staleAfter
	}
	res := CapacityResult{Matched: []HostView{}, Unknown: []HostVerdict{}, MaxStaleAge: maxAge.String(), Total: len(views)}
	now := s.now()
	nExcluded := 0
	for _, v := range views {
		excluded, unknown := s.judge(v, q, maxAge, now)
		x, u, facts := s.judgeParts(ctx, v, q, maxAge, now)
		excluded, unknown = append(excluded, x...), append(unknown, u...)
		verdict := func(reasons []string) HostVerdict {
			h := HostVerdict{Name: v.Host.Name, State: v.State, ProbedAt: v.ProbedAt, Reasons: reasons}
			if v.ProbedAt != nil {
				age := int64(now.Sub(*v.ProbedAt).Seconds())
				h.AgeSecond = &age
			}
			return h
		}
		switch {
		case len(excluded) > 0:
			nExcluded++
			if q.Explain {
				res.Excluded = append(res.Excluded, verdict(excluded))
			}
		case len(unknown) > 0:
			u := verdict(unknown)
			u.NextStep = fmt.Sprintf("run `cerberus host probe %s` (or the cerberus_host_probe tool) for fresh facts", v.Host.Name)
			res.Unknown = append(res.Unknown, u)
		default:
			res.Matched = append(res.Matched, v)
			if facts != nil {
				if res.Judged == nil {
					res.Judged = map[string]RecentFacts{}
				}
				res.Judged[v.Host.Name] = *facts
			}
		}
	}
	call.note(fmt.Sprintf("hosts=%d matched=%d unknown=%d excluded=%d", res.Total, len(res.Matched), len(res.Unknown), nExcluded))
	return res, nil
}

// judge returns the reasons a host definitely fails the query, and the
// reasons it cannot be judged. A host with any definite failure is excluded.
func (s *HostService) judge(v HostView, q CapacityQuery, maxAge time.Duration, now time.Time) (excluded, unknown []string) {
	h := v.Host
	if q.Role != "" && h.Role != q.Role {
		excluded = append(excluded, fmt.Sprintf("role is %s, not %s", orNone(h.Role), q.Role))
	}
	if q.Provider != "" && h.Provider != q.Provider {
		excluded = append(excluded, fmt.Sprintf("provider is %s, not %s", orNone(h.Provider), q.Provider))
	}
	keys := make([]string, 0, len(q.Labels))
	for k := range q.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if got, ok := h.Labels[k]; !ok {
			excluded = append(excluded, fmt.Sprintf("label %s is not set", k))
		} else if got != q.Labels[k] {
			excluded = append(excluded, fmt.Sprintf("label %s is %s, not %s", k, got, q.Labels[k]))
		}
	}
	if v.State == hosts.StateOffline && !q.IncludeOffline {
		excluded = append(excluded, "the host is offline")
	}
	if !q.measured() {
		return excluded, nil
	}

	p := v.Probed
	switch {
	case p == nil || v.ProbedAt == nil:
		return excluded, append(unknown, "the host has no probe data")
	case now.Sub(*v.ProbedAt) > maxAge:
		return excluded, append(unknown, fmt.Sprintf("the probe data is %s old, beyond the %s limit",
			now.Sub(*v.ProbedAt).Round(time.Second), maxAge))
	}
	check := func(name string, need, have int64, reported bool, unit string) {
		if need <= 0 {
			return
		}
		switch {
		case !reported:
			unknown = append(unknown, "the probe did not report "+name)
		case have < need:
			excluded = append(excluded, fmt.Sprintf("%s is %d%s, below the %d%s required", name, have, unit, need, unit))
		}
	}
	check("memory_available", int64(q.MinMemoryAvailable), p.MemoryAvailable, p.MemoryAvailable > 0, " bytes")
	check("disk_free", int64(q.MinDiskFree), p.DiskFree, p.DiskTotal > 0, " bytes")
	check("cpu", int64(q.MinCPU), int64(p.CPU), p.CPU > 0, "")
	for _, t := range q.RequireTools {
		have, reported := p.Tools[t]
		switch {
		case !reported:
			unknown = append(unknown, "the probe did not report whether "+t+" is installed")
		case !have:
			excluded = append(excluded, t+" is not installed")
		}
	}
	return excluded, unknown
}

// judgeParts tests the Hosts F1 constraints: hardware, monitor status and
// recent headroom. A fact that is not recorded makes the host unknown for that
// constraint; a recorded fact that fails excludes it.
func (s *HostService) judgeParts(ctx context.Context, v HostView, q CapacityQuery, maxAge time.Duration, now time.Time) (excluded, unknown []string, facts *RecentFacts) {
	h := v.Host
	hw := h.Hardware
	fail := func(f string, a ...any) { excluded = append(excluded, fmt.Sprintf(f, a...)) }
	miss := func(f string, a ...any) { unknown = append(unknown, fmt.Sprintf(f, a...)) }

	if q.MinMemoryTotal > 0 {
		var total int64
		switch {
		case v.Probed != nil && v.ProbedAt != nil && now.Sub(*v.ProbedAt) <= maxAge && v.Probed.MemoryTotal > 0:
			total = v.Probed.MemoryTotal
		case hw != nil && hw.Memory != nil && hw.Memory.Total > 0:
			total = int64(hw.Memory.Total)
		}
		switch {
		case total == 0:
			miss("installed memory is neither probed (fresh) nor recorded")
		case total < int64(q.MinMemoryTotal):
			fail("installed memory is %d bytes, below the %d required", total, int64(q.MinMemoryTotal))
		}
	}
	if q.MinMemoryUpgrade > 0 {
		var installed, ceiling int64
		if hw != nil && hw.Memory != nil {
			installed = int64(hw.Memory.Total)
		}
		if hw != nil && hw.Ceilings != nil && hw.Ceilings.Memory != nil && hw.Ceilings.Memory.MaxTotal != nil {
			ceiling = int64(hw.Ceilings.Memory.MaxTotal.Value)
		}
		switch {
		case installed == 0 || ceiling == 0:
			miss("the memory ceiling or the installed memory is not recorded, so the room to add memory is unknown")
		case ceiling-installed < int64(q.MinMemoryUpgrade):
			fail("room to add memory is %d bytes (ceiling %d, installed %d), below the %d required", ceiling-installed, ceiling, installed, int64(q.MinMemoryUpgrade))
		}
	}
	if q.FreeMemorySlots > 0 {
		switch {
		case hw == nil || hw.Memory == nil || hw.Memory.Slots == 0:
			miss("the number of memory slots is not recorded")
		case hw.Memory.Slots-hw.Memory.SlotsUsed < q.FreeMemorySlots:
			fail("%d of %d memory slots are free, %d required", hw.Memory.Slots-hw.Memory.SlotsUsed, hw.Memory.Slots, q.FreeMemorySlots)
		}
	}
	for _, need := range q.FreeStorageSlots {
		if v.StorageSummary == nil {
			miss("storage slots are not recorded, so free %s slots are unknown", need.Kind)
			continue
		}
		if free := v.StorageSummary[need.Kind].Free; free < need.Min {
			fail("%d free %s slots, %d required", free, need.Kind, need.Min)
		}
	}
	if len(q.DiskTypes) > 0 || q.MinDiskSize > 0 {
		switch {
		case hw == nil || len(hw.Disks) == 0:
			miss("disks are not recorded")
		default:
			okType, okSize := len(q.DiskTypes) == 0, q.MinDiskSize == 0
			for _, d := range hw.Disks {
				typeOK := len(q.DiskTypes) == 0 || slices.Contains(q.DiskTypes, d.Type)
				sizeOK := q.MinDiskSize == 0 || d.Size >= q.MinDiskSize
				okType = okType || typeOK
				okSize = okSize || sizeOK
			}
			if !okType {
				fail("no disk of type %s", strings.Join(q.DiskTypes, " or "))
			}
			if !okSize {
				fail("no disk of at least %d bytes", int64(q.MinDiskSize))
			}
		}
	}
	if len(q.StatusIn) > 0 {
		switch {
		case v.Status == nil:
			miss("the host has no monitor status (monitoring is off or has not reached it)")
		case !slices.Contains(q.StatusIn, v.Status.Status):
			fail("status is %s, not %s", v.Status.Status, strings.Join(q.StatusIn, " or "))
		}
	}
	if r := q.Recent; r != nil {
		x, u, f := s.judgeRecent(ctx, v, *r, now)
		excluded, unknown, facts = append(excluded, x...), append(unknown, u...), f
	}
	return excluded, unknown, facts
}

// judgeRecent tests headroom against the worst sample in the window.
func (s *HostService) judgeRecent(ctx context.Context, v HostView, r RecentNeed, now time.Time) (excluded, unknown []string, facts *RecentFacts) {
	if s.mon == nil {
		return nil, []string{"no samples are kept: host monitoring is not available"}, nil
	}
	interval, monitored := s.mon.settings.IntervalFor(v.Host)
	if !monitored {
		return nil, []string{"the host is not monitored, so there are no samples"}, nil
	}
	samples, err := s.mon.store.Samples(ctx, v.Host.Name, now.Add(-r.Window), now, 20000)
	if err != nil {
		return nil, []string{"samples could not be read: " + redact.Text(err.Error())}, nil
	}
	expected := int(r.Window / interval)
	if expected < 1 {
		expected = 1
	}
	if len(samples) == 0 || len(samples)*2 < expected {
		return nil, []string{fmt.Sprintf("%d of %d expected samples in the last %s", len(samples), expected, r.Window)}, nil
	}
	f := RecentFacts{Window: r.Window.String(), Samples: len(samples), Expected: expected}
	for i, x := range samples {
		if x.Load1 != nil && x.CPU > 0 {
			if l := *x.Load1 / float64(x.CPU); l > f.WorstLoadPerCore {
				f.WorstLoadPerCore = l
			}
		}
		if x.MemoryAvailable > 0 && (i == 0 || f.MinMemoryAvailable == 0 || x.MemoryAvailable < f.MinMemoryAvailable) {
			f.MinMemoryAvailable = x.MemoryAvailable
		}
		if x.DiskFree > 0 && (i == 0 || f.MinDiskFree == 0 || x.DiskFree < f.MinDiskFree) {
			f.MinDiskFree = x.DiskFree
		}
	}
	if r.MaxLoadPerCore > 0 {
		if f.WorstLoadPerCore == 0 {
			unknown = append(unknown, "the samples in the window reported no load")
		} else if f.WorstLoadPerCore > r.MaxLoadPerCore {
			excluded = append(excluded, fmt.Sprintf("load per core reached %.2f in the last %s, above the %.2f allowed", f.WorstLoadPerCore, r.Window, r.MaxLoadPerCore))
		}
	}
	if r.MinMemoryAvailable > 0 && f.MinMemoryAvailable < int64(r.MinMemoryAvailable) {
		excluded = append(excluded, fmt.Sprintf("memory available fell to %d bytes in the last %s, below the %d required", f.MinMemoryAvailable, r.Window, int64(r.MinMemoryAvailable)))
	}
	if r.MinDiskFree > 0 && f.MinDiskFree < int64(r.MinDiskFree) {
		excluded = append(excluded, fmt.Sprintf("free disk fell to %d bytes in the last %s, below the %d required", f.MinDiskFree, r.Window, int64(r.MinDiskFree)))
	}
	return excluded, unknown, &f
}

func orNone(s string) string {
	if s == "" {
		return "not set"
	}
	return s
}

// HostClient is what a surface needs of the host service: the same four reads
// on the in-process client and over the socket. It is separate from Client so
// older fakes keep compiling.
type HostClient interface {
	ListHosts(ctx context.Context, filter HostFilter) ([]HostView, error)
	GetHost(ctx context.Context, name string) (HostView, error)
	ProbeHost(ctx context.Context, name string) (HostView, error)
	HostCapacity(ctx context.Context, q CapacityQuery) (CapacityResult, error)
}

// ComponentClient is the component reads (Hosts B). It is separate from
// HostClient so older fakes keep compiling.
type ComponentClient interface {
	ListComponents(ctx context.Context, filter ComponentFilter) ([]hosts.Component, error)
	GetComponent(ctx context.Context, id string) (hosts.Component, error)
}

// ComponentClientOf returns client as a ComponentClient, or one that refuses
// every call with the reason.
func ComponentClientOf(client any) ComponentClient {
	if c, ok := client.(ComponentClient); ok {
		return c
	}
	return unavailableHostClient{}
}

func (unavailableHostClient) ListComponents(context.Context, ComponentFilter) ([]hosts.Component, error) {
	return nil, errHostsUnavailable
}
func (unavailableHostClient) GetComponent(context.Context, string) (hosts.Component, error) {
	return hosts.Component{}, errHostsUnavailable
}

// HostClientOf returns client as a HostClient, or one that refuses every call
// with the reason, so a tool built over a client without host support still
// lists and explains itself.
func HostClientOf(client any) HostClient {
	if h, ok := client.(HostClient); ok {
		return h
	}
	return unavailableHostClient{}
}

var errHostsUnavailable = redact.Guidance("host support is not available from this serving host; run the daemon from this build")

type unavailableHostClient struct{}

func (unavailableHostClient) ListHosts(context.Context, HostFilter) ([]HostView, error) {
	return nil, errHostsUnavailable
}
func (unavailableHostClient) GetHost(context.Context, string) (HostView, error) {
	return HostView{}, errHostsUnavailable
}
func (unavailableHostClient) ProbeHost(context.Context, string) (HostView, error) {
	return HostView{}, errHostsUnavailable
}
func (unavailableHostClient) HostCapacity(context.Context, CapacityQuery) (CapacityResult, error) {
	return CapacityResult{}, errHostsUnavailable
}

// Host operation names, shared by the MCP tools and the contract.
const (
	OpHostList     = "list"
	OpHostGet      = "get"
	OpHostProbe    = "probe"
	OpHostCapacity = "capacity"

	OpComponentList = "component_list"
	OpComponentGet  = "component_get"
)

func fromFields(from string) []string {
	if from == "" {
		return nil
	}
	return []string{from}
}

// HostDefinition is the contract of the host operations. All four are reads:
// a probe measures, it never changes the host.
func HostDefinition() contract.Definition {
	read := func(name, desc string, from []string, inputs ...contract.Input) contract.Operation {
		return contract.Operation{
			Name: name, Description: desc, Effect: contract.EffectRead,
			Target:  contract.TargetDescriptor{Kind: "host", From: from},
			Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone,
			Inputs: inputs,
		}
	}
	write := func(name, kind, from, desc string, inputs ...contract.Input) contract.Operation {
		return contract.Operation{
			Name: name, Description: desc, Effect: contract.EffectWrite, Reversible: true,
			Target:  contract.TargetDescriptor{Kind: kind, From: fromFields(from)},
			Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSWrites,
			Inputs: inputs,
		}.Finalize()
	}
	labels := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Labels that must all match exactly."}
	size := func(desc string) map[string]any {
		return map[string]any{"type": []string{"string", "integer"}, "description": desc}
	}
	name := contract.RequiredField("name", contract.StringSchema("Host name."))
	return contract.Finalize(contract.Definition{ID: "host", Version: "builtin", ResourceTypes: []string{"host"}, Operations: []contract.Operation{
		read(OpHostList, "List declared hosts with their recorded state and probed facts.", nil,
			contract.Field("role", contract.StringSchema("Only hosts with this role.")),
			contract.Field("provider", contract.StringSchema("Only hosts with this provider.")),
			contract.Field("state", contract.StringSchema("online, offline or unknown.")),
			contract.Field("labels", labels)),
		read(OpHostGet, "Show one host.", []string{"name"}, name),
		read(OpHostProbe, "Measure one host over ssh and record the facts. A read: the commands are fixed.", []string{"name"}, name),
		read(OpComponentList, "List spare and installed parts (components) recorded in hosts.yaml.", nil,
			contract.Field("kind", contract.StringSchema("ram, disk, cpu or other.")),
			contract.Field("host", contract.StringSchema("Only components located on this host.")),
			contract.Field("spare", map[string]any{"type": "boolean", "description": "Only spare components."})),
		read(OpComponentGet, "Show one component.", nil, contract.RequiredField("id", contract.StringSchema("Component id."))),
		write(OpHostUpdate, "host", "name", "Change one host with a merge patch (labels, notes, role, hardware, ceilings, storage_slots, ...). Validated as a whole file; needs the revision you read.",
			contract.RequiredField("name", contract.StringSchema("Host name.")),
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read.")),
			contract.Field("patch", map[string]any{"type": "object", "description": "JSON merge patch: objects merge, null deletes, lists replace."}),
			contract.Field("create", map[string]any{"type": "boolean", "description": "Create the host when it does not exist."})),
		write(OpHostRemove, "host", "name", "Remove a host no component refers to.",
			contract.RequiredField("name", contract.StringSchema("Host name.")),
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read."))),
		write(OpComponentCreate, "component", "", "Add a spare or installed part.",
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read.")),
			contract.Field("component", map[string]any{"type": "object", "description": "id, kind, size, type, speed, serial, location, notes."})),
		write(OpComponentUpdate, "component", "id", "Change a part's fields (not its location).",
			contract.RequiredField("id", contract.StringSchema("Component id.")),
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read.")),
			contract.Field("patch", map[string]any{"type": "object", "description": "JSON merge patch."})),
		write(OpComponentMove, "component", "id", "Move a part to a slot on a host, or to the spares.",
			contract.RequiredField("id", contract.StringSchema("Component id.")),
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read.")),
			contract.Field("to", map[string]any{"type": "object", "description": "{host, slot} or {spare: true}."})),
		write(OpComponentRemove, "component", "id", "Delete a part.",
			contract.RequiredField("id", contract.StringSchema("Component id.")),
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read."))),
		read(OpHostHistory, "Changes that touched a host, newest first.", []string{"name"}, name,
			contract.Field("limit", contract.IntegerSchema("Most entries to return."))),
		read(OpComponentHistory, "Changes to one component, newest first.", []string{"id"},
			contract.RequiredField("id", contract.StringSchema("Component id.")),
			contract.Field("limit", contract.IntegerSchema("Most entries to return."))),
		write(OpHostInventory, "host", "name", "Collect the host's physical facts over ssh (sudo -n for root-only ones) and record them; operator-set notes, storage_slots, vendor-sourced ceilings and components are kept. revision is optional.",
			contract.RequiredField("name", contract.StringSchema("Host name.")),
			contract.Field("revision", contract.StringSchema("Optional: the hosts.yaml revision you read.")),
			contract.Field("dry_run", map[string]any{"type": "boolean", "description": "Collect and show what would change; write nothing."})),
		read(OpHostStatus, "Health of one host (online, degraded, offline, unknown) with reasons, or of every host when no name is given.", nil,
			contract.Field("name", contract.StringSchema("Host name."))),
		read(OpHostSamples, "A host's retained measurements in time order.", []string{"name"},
			contract.RequiredField("name", contract.StringSchema("Host name.")),
			contract.Field("since", contract.StringSchema("RFC 3339 time or a duration such as 6h.")),
			contract.Field("until", contract.StringSchema("RFC 3339 time.")),
			contract.Field("limit", contract.IntegerSchema("Most samples (default 500)."))),
		read(OpHostEvents, "Status-change events after an id, oldest first, with the id to resume from.", nil,
			contract.Field("after_id", contract.IntegerSchema("Resume after this event id.")),
			contract.Field("host", contract.StringSchema("Only this host.")),
			contract.Field("kind", contract.StringSchema("Only this kind.")),
			contract.Field("limit", contract.IntegerSchema("Most events (default 200)."))),
		read(OpHostMonitor, "What the host monitor is doing: settings and each host's interval and next probe.", nil),
		read(OpMonitorEvent, "A record of the host monitor acting on its own: a status change, a failed-probe streak, an hourly summary.", nil),
		read(OpHostRevision, "The current revision of hosts.yaml: the token every write carries.", nil),
		read(OpHostCapacity, "List the hosts that satisfy constraints, from recorded facts. Facts only: no ranking.", nil,
			contract.Field("min_memory_available", size("Bytes or a size such as 4GiB.")),
			contract.Field("min_disk_free", size("Bytes or a size such as 50GB.")),
			contract.Field("min_cpu", contract.IntegerSchema("Minimum probed logical cores.")),
			contract.Field("role", contract.StringSchema("Declared role.")),
			contract.Field("provider", contract.StringSchema("Provider.")),
			contract.Field("labels", labels),
			contract.Field("require_tools", map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Probed tools that must be installed."}),
			contract.Field("max_stale_age", contract.StringSchema("Oldest probe a measured constraint is judged on, such as 10m.")),
			contract.Field("include_offline", map[string]any{"type": "boolean", "description": "Judge offline hosts on their last measurement."}),
			contract.Field("explain", map[string]any{"type": "boolean", "description": "Also list excluded hosts with reasons."}),
			contract.Field("min_memory_total", size("Installed memory at least this.")),
			contract.Field("min_memory_upgrade", size("Room to add at least this much memory.")),
			contract.Field("free_memory_slots", contract.IntegerSchema("At least this many empty memory slots.")),
			contract.Field("free_storage_slots", map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "Free storage slots required by kind: [{kind, min}]."}),
			contract.Field("disk_types", map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "At least one recorded disk of these types."}),
			contract.Field("min_disk_size", size("At least one recorded disk at least this large.")),
			contract.Field("status_in", map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Monitor status must be one of these."}),
			contract.Field("recent", map[string]any{"type": "object", "description": "Headroom held over a window: window, max_load_per_core, min_memory_available, min_disk_free."})),
	}})
}
