package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
}

// MarshalJSON writes the wire form.
func (q CapacityQuery) MarshalJSON() ([]byte, error) {
	w := capacityQueryWire{MinMemoryAvailable: q.MinMemoryAvailable, MinCPU: q.MinCPU, MinDiskFree: q.MinDiskFree,
		Role: q.Role, Provider: q.Provider, Labels: q.Labels, RequireTools: q.RequireTools,
		IncludeOffline: q.IncludeOffline, Explain: q.Explain}
	if q.MaxStaleAge > 0 {
		w.MaxStaleAge = q.MaxStaleAge.String()
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
		return &HostInvalidError{Err: redact.Guidance("the capacity query must be a JSON object of its documented fields (min_memory_available, min_cpu, min_disk_free, role, provider, labels, require_tools, max_stale_age, include_offline, explain)")}
	}
	out := CapacityQuery{MinMemoryAvailable: w.MinMemoryAvailable, MinCPU: w.MinCPU, MinDiskFree: w.MinDiskFree,
		Role: w.Role, Provider: w.Provider, Labels: w.Labels, RequireTools: w.RequireTools,
		IncludeOffline: w.IncludeOffline, Explain: w.Explain}
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
}

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
			contract.Field("explain", map[string]any{"type": "boolean", "description": "Also list excluded hosts with reasons."})),
	}})
}
