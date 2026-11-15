package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

// HostSource reads the declared hosts (hosts.yaml) and the last known state of each, without
// writing either. The host monitor and probes arrive with slice 8.
type HostSource interface {
	Declared() (*hosts.File, error)
	State(name string) hosts.State
}

// FileHosts reads the adopted state tree in place: <adopt_root>/hosts.yaml and
// <adopt_root>/state/hosts.json. A missing hosts.yaml is an empty list.
type FileHosts struct {
	HostsPath, StatePath string
}

// Declared loads hosts.yaml.
func (f FileHosts) Declared() (*hosts.File, error) { return hosts.Load(f.HostsPath) }

// State is the stored state of name; unknown when there is none. It never writes.
func (f FileHosts) State(name string) hosts.State { return hosts.NewStore(f.StatePath).Get(name) }

// hostFilter is the old HostFilter: role, provider, state and labels.
type hostFilter struct {
	Role, Provider, State string
	Labels                map[string]string
}

func hostFilterFrom(r *http.Request) (hostFilter, error) {
	q := r.URL.Query()
	f := hostFilter{Role: q.Get("role"), Provider: q.Get("provider"), State: q.Get("state")}
	for _, l := range q["label"] {
		k, v, ok := strings.Cut(l, "=")
		if !ok || k == "" {
			return f, fmt.Errorf("%w: a label filter is key=value", errBadArgs)
		}
		if f.Labels == nil {
			f.Labels = map[string]string{}
		}
		f.Labels[k] = v
	}
	return f, nil
}

func (f hostFilter) matches(v HostView) bool {
	if (f.Role != "" && v.Host.Role != f.Role) || (f.Provider != "" && v.Host.Provider != f.Provider) ||
		(f.State != "" && v.State != f.State) {
		return false
	}
	for k, want := range f.Labels {
		if got, ok := v.Host.Labels[k]; !ok || got != want {
			return false
		}
	}
	return true
}

// hostView is the old HostService.view without the monitor (slice 8): declared data, the
// stored state, and staleness against DefaultStaleAfter.
func hostView(h hosts.Host, comps []hosts.Component, st hosts.State, now time.Time) HostView {
	v := HostView{Host: h, State: st.State, OnboardingStage: st.OnboardingStage, Reason: st.Reason,
		LastSeen: st.LastSeen, ProbedAt: st.ProbedAt, CheckedAt: st.CheckedAt, Probed: st.Probed}
	if h.Hardware != nil {
		v.Ceilings = h.Hardware.Ceilings
		v.StorageSlots = h.Hardware.StorageSlots
		v.StorageSummary = hosts.StorageSummary(h.Hardware.StorageSlots)
	}
	v.Warnings = hosts.SlotWarnings(h)
	for _, c := range comps {
		if c.Location.Host == h.Name {
			v.Components = append(v.Components, c)
		}
	}
	if v.State == "" {
		v.State = hosts.StateUnknown
	}
	stale := hosts.DefaultStaleAfter
	v.Stale = st.CheckedAt == nil || now.Sub(*st.CheckedAt) > stale ||
		(st.Probed != nil && (st.ProbedAt == nil || now.Sub(*st.ProbedAt) > stale))
	return v
}

func (a *API) hosts(w http.ResponseWriter, r *http.Request) {
	f, err := hostFilterFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := []HostView{}
	if a.o.Hosts != nil {
		file, err := a.o.Hosts.Declared()
		if err != nil {
			writeErr(w, err)
			return
		}
		now := time.Now()
		for _, h := range file.Hosts {
			if v := hostView(h, file.Components, a.o.Hosts.State(h.Name), now); f.matches(v) {
				out = append(out, v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host.Name < out[j].Host.Name })
	writeJSON(w, out)
}

// resourceToDomain is the connector's view of a registry resource.
func resourceToDomain(r config.ResourceDef) *domain.Resource {
	return &domain.Resource{
		ID: r.ID, Name: r.Name, Type: domain.ResourceType(r.Type), ProjectID: r.Project,
		Connector: r.Connector, Config: r.Config,
		Tags: append([]string(nil), r.Tags...), DependsOn: append([]string(nil), r.DependsOn...),
	}
}
