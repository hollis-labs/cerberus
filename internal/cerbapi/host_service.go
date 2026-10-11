package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// HostService is the admin-lane service for hosts (ADR 0004). A host is a
// named target Cerberus keeps a record of; it is not a supervised resource,
// and nothing here ranks, reserves or places. Surfaces (CLI, socket, HTTP,
// console, MCP) are thin callers of List, Get and Probe.
//
// Declared data is read from the operator's hosts.yaml on every call, so an
// edit takes effect without a restart. Derived data (probe results, state,
// last_seen) is the daemon's own and is rebuilt by probing.
type HostService struct {
	audit      audit.Sink
	logger     *slog.Logger
	hostsPath  string
	store      *hosts.Store
	resources  ResourceLookup
	ssh        func(ctx context.Context) (HostSSH, error)
	staleAfter time.Duration
	now        func() time.Time
	probeLimit time.Duration
	mu         sync.Mutex // serializes probes
}

// HostSSH is the part of the ssh connector a probe uses.
type HostSSH interface {
	Exec(ctx context.Context, res *resource.Resource, command string) (*sshconn.ExecResult, error)
}

// HostServiceOptions configure NewHostService. Zero values take defaults.
type HostServiceOptions struct {
	// HostsPath is hosts.yaml; empty is ~/.cerberus/hosts.yaml.
	HostsPath string
	// StatePath is the derived-state file; empty is ~/.cerberus/state/hosts.json.
	// Use "-" for in-memory state.
	StatePath string
	// Resources resolves a host's ssh reach.
	Resources ResourceLookup
	// SSH returns the ssh connector, resolved per call.
	SSH func(ctx context.Context) (HostSSH, error)
	// StaleAfter is the age past which derived data reports stale.
	StaleAfter time.Duration
	Now        func() time.Time
}

// NewHostService constructs the host service. The audit sink is required for
// the same reason the admin lane's is. Outside tests it is constructed only in
// internal/app.
func NewHostService(sink audit.Sink, opts HostServiceOptions) *HostService {
	if sink == nil {
		panic("cerbapi: NewHostService requires an audit sink")
	}
	s := &HostService{audit: sink, logger: slog.Default(), resources: opts.Resources, ssh: opts.SSH,
		staleAfter: opts.StaleAfter, now: opts.Now, hostsPath: opts.HostsPath, probeLimit: 45 * time.Second}
	if s.staleAfter <= 0 {
		s.staleAfter = hosts.DefaultStaleAfter
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.hostsPath == "" {
		s.hostsPath, _ = hosts.DefaultPath()
	}
	statePath := opts.StatePath
	switch statePath {
	case "":
		statePath, _ = hosts.DefaultStatePath()
	case "-":
		statePath = ""
	}
	s.store = hosts.NewStore(statePath)
	return s
}

// HostView is the DTO every surface returns for a host. Declared and probed
// data sit side by side and are never merged.
type HostView struct {
	// Host is the declared record, including declared specs.
	Host hosts.Host `json:"host"`
	// State is online, offline or unknown.
	State           string `json:"state"`
	OnboardingStage string `json:"onboarding_stage,omitempty"`
	Reason          string `json:"reason,omitempty"`
	// LastSeen is the last time the host answered a probe.
	LastSeen *time.Time `json:"last_seen,omitempty"`
	// ProbedAt is when Probed was measured; CheckedAt the last attempt.
	ProbedAt  *time.Time    `json:"probed_at,omitempty"`
	CheckedAt *time.Time    `json:"checked_at,omitempty"`
	Stale     bool          `json:"stale"`
	Probed    *hosts.Probed `json:"probed,omitempty"`
}

// HostFilter selects hosts by facts the operator recorded. Matching is exact,
// results are in name order, and there is deliberately no ranking.
type HostFilter struct {
	Role     string            `json:"role,omitempty"`
	Provider string            `json:"provider,omitempty"`
	State    string            `json:"state,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// HostNotFoundError is returned for an unknown host name.
type HostNotFoundError struct{ Name string }

func (e *HostNotFoundError) Error() string {
	return fmt.Sprintf("host %q is not declared; see `cerberus host list` (hosts are declared in hosts.yaml)", e.Name)
}

func (s *HostService) load() (*hosts.File, error) {
	if s == nil {
		return nil, errors.New("host service is not configured")
	}
	return hosts.Load(s.hostsPath)
}

func (s *HostService) view(h hosts.Host) HostView {
	st := s.store.Get(h.Name)
	v := HostView{Host: h, State: st.State, OnboardingStage: st.OnboardingStage, Reason: st.Reason,
		LastSeen: st.LastSeen, ProbedAt: st.ProbedAt, CheckedAt: st.CheckedAt, Probed: st.Probed}
	if v.State == "" {
		v.State = hosts.StateUnknown
	}
	now := s.now()
	v.Stale = st.CheckedAt == nil || now.Sub(*st.CheckedAt) > s.staleAfter ||
		(st.Probed != nil && (st.ProbedAt == nil || now.Sub(*st.ProbedAt) > s.staleAfter))
	return v
}

// List returns the declared hosts with their derived state, in name order.
// A hosts.yaml that fails validation is an error naming every problem.
func (s *HostService) List(_ context.Context, filter HostFilter) ([]HostView, error) {
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	out := make([]HostView, 0, len(f.Hosts))
	for _, h := range f.Hosts {
		keep[h.Name] = true
		v := s.view(h)
		if filter.matches(v) {
			out = append(out, v)
		}
	}
	// Derived data for a host no longer declared is dropped.
	_ = s.store.Forget(keep)
	sort.Slice(out, func(i, j int) bool { return out[i].Host.Name < out[j].Host.Name })
	return out, nil
}

func (f HostFilter) matches(v HostView) bool {
	if f.Role != "" && v.Host.Role != f.Role {
		return false
	}
	if f.Provider != "" && v.Host.Provider != f.Provider {
		return false
	}
	if f.State != "" && v.State != f.State {
		return false
	}
	for k, want := range f.Labels {
		if got, ok := v.Host.Labels[k]; !ok || got != want {
			return false
		}
	}
	return true
}

// Get returns one host.
func (s *HostService) Get(ctx context.Context, name string) (HostView, error) {
	f, err := s.load()
	if err != nil {
		return HostView{}, err
	}
	for _, h := range f.Hosts {
		if h.Name == name {
			return s.view(h), nil
		}
	}
	return HostView{}, &HostNotFoundError{Name: name}
}

// hostProbeOperation is the contract the audit log records a probe under: a
// read of one host.
func hostProbeOperation() contract.Operation {
	return contract.Operation{
		Name: "probe", Effect: contract.EffectRead,
		Target:  contract.TargetDescriptor{Kind: "host", From: []string{"name"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone,
	}.Finalize()
}

// Probe measures a host over ssh and records the result as its derived state.
// It is a read: the commands are fixed by Cerberus and take no caller input.
// An unreachable host is a state (offline), not an error; an error means the
// probe could not be attempted (unknown host, no usable ssh reach, audit
// unavailable).
func (s *HostService) Probe(ctx context.Context, name string) (_ HostView, retErr error) {
	declared, err := s.Get(ctx, name)
	if err != nil {
		return HostView{}, err
	}
	h := declared.Host
	ctx, scope := redact.EnsureScope(ctx)

	spec := auditSpec{connector: "host", operation: "probe", op: hostProbeOperation(), known: true,
		config: map[string]any{"name": name}, resources: s.resources}
	call, err := beginGated(ctx, s.audit, s.logger, spec)
	if err != nil {
		return HostView{}, err
	}
	defer func() { call.finish(retErr) }()

	def, err := s.reach(h)
	if err != nil {
		return HostView{}, err
	}
	if s.ssh == nil {
		return HostView{}, redact.Guidance("host %q cannot be probed: the ssh connector is not available", name)
	}

	// The ssh credentials resolve for a read of the reach resource.
	sshSpec := auditSpec{connector: "ssh", operation: "exec", op: hostProbeOperation(), known: true,
		config: map[string]any{"id": def.ID}, resources: s.resources}
	pctx, cancel := context.WithTimeout(withCredentialScope(call.withTelemetry(ctx), sshSpec), s.probeLimit)
	defer cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	prev := s.store.Get(name)
	next := prev
	next.CheckedAt = &now

	probed, perr := s.gather(pctx, def)
	switch {
	case perr != nil:
		next.State = hosts.StateOffline
		next.Reason = hostReason(scope, "the host did not answer over ssh", perr)
	case probed.Empty():
		next.State = hosts.StateUnknown
		next.Reason = "the host answered but reported no usable facts"
	default:
		next.State = hosts.StateOnline
		next.Reason = ""
		next.LastSeen = &now
		next.ProbedAt = &now
		next.Probed = probed
	}
	if err := s.store.Put(name, next); err != nil {
		s.logger.Warn("host.state_write_failed", "host", name, "error", scope.Text(err.Error()))
	}
	return s.view(h), nil
}

// reach resolves a host's ssh resource. A host with no usable reach cannot be
// probed; that is a configuration problem, not a host state.
func (s *HostService) reach(h hosts.Host) (*config.ResourceDef, error) {
	if h.Reach.SSH == "" {
		return nil, redact.Guidance("host %q declares no reach.ssh, so it cannot be probed; set reach.ssh to a server/ssh resource id in hosts.yaml", h.Name)
	}
	if s.resources == nil {
		return nil, redact.Guidance("host %q cannot be probed: no resource configuration is loaded", h.Name)
	}
	def, ok := s.resources(h.Reach.SSH)
	if !ok || def == nil {
		return nil, redact.Guidance("host %q reaches ssh resource %q, which is not in the loaded config; run `cerberus resource list`", h.Name, h.Reach.SSH)
	}
	if def.Connector != "ssh" {
		return nil, redact.Guidance("host %q reaches resource %q, which uses connector %q, not ssh", h.Name, def.ID, def.Connector)
	}
	return def, nil
}

// gather measures the host with plain POSIX commands over ssh. An error
// means the host could not be reached; a host that answers badly yields an
// empty Probed, not an error.
func (s *HostService) gather(ctx context.Context, def *config.ResourceDef) (*hosts.Probed, error) {
	sshc, err := s.ssh(ctx)
	if err != nil {
		return nil, err
	}
	cfg := make(map[string]any, len(def.Config)+2)
	for k, v := range def.Config {
		cfg[k] = v
	}
	cfg["id"], cfg["name"] = def.ID, def.Name
	res := resource.Resource{ID: def.ID, Name: def.Name, Type: resource.Server, Connector: "ssh", Config: cfg}
	out, err := sshc.Exec(ctx, &res, hosts.PosixProbeCommand)
	if err != nil {
		return nil, err
	}
	p := hosts.ParsePosixProbe(out.Stdout)
	return &p, nil
}

// hostReason is the reason text stored with an offline host: Cerberus's
// words, then the cause through the request's scope and the rules, bounded.
func hostReason(scope *redact.Scope, lead string, cause error) string {
	text := strings.Join(strings.Fields(scope.Text(cause.Error())), " ")
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return lead + ": " + text
}
