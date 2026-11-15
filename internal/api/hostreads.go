package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/hostmon"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

// The old socket's host reads beyond L9 (internal/cerbapi host_http.go, host_monitor_http.go,
// host_events.go on main), with its shapes, and GET /api/projects (console contract §3.3). Every
// one is cerberus.read. In supervise mode each host read is recorded through the gate as main's
// beginRead recorded it (an EffectRead operation of the host contract); in observe mode there is
// no audit chain, so they are not recorded, as L6.

// ProjectInfo is a project as the old project list answered it (main's cerbapi.ProjectInfo).
type ProjectInfo struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"`
	Resources    int           `json:"resource_count"`
	Capabilities []string      `json:"capabilities,omitempty"`
	Links        []config.Link `json:"links,omitempty"`
}

// The host read shapes are main's, defined beside the reads (internal/hostmon).
type (
	HostStatusView   = hostmon.HostStatusView
	MonitorSnapshot  = hostmon.MonitorSnapshot
	MonitorHostState = hostmon.MonitorHostState
	EventsResult     = hostmon.EventsResult
)

func (a *API) projects(w http.ResponseWriter, r *http.Request) {
	out := []ProjectInfo{}
	if a.o.Projects != nil {
		projects, err := a.o.Projects()
		if err != nil {
			writeErr(w, err)
			return
		}
		defs, err := a.o.Resources()
		if err != nil {
			writeErr(w, err)
			return
		}
		counts := make(map[string]int, len(projects))
		for _, r := range defs {
			counts[r.Project]++
		}
		for _, p := range projects {
			out = append(out, ProjectInfo{ID: p.ID, Name: p.Name, Description: p.Description, Resources: counts[p.ID],
				Capabilities: append([]string(nil), p.Capabilities...), Links: append([]config.Link(nil), p.Links...)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeScoped(GateContext(r.Context()), w, out)
}

// hostErr maps the host reads' errors onto the flat body.
func hostErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hostmon.ErrNotFound):
		writeErr(w, fmt.Errorf("%w: %s", errNotFound, err.Error()))
	case errors.Is(err, hostmon.ErrInvalid):
		writeErr(w, fmt.Errorf("%w: %s", errBadArgs, err.Error()))
	case errors.Is(err, hostmon.ErrConflict):
		apierr.Write(w, http.StatusConflict, "revision_conflict", err.Error(), nil)
	case errors.Is(err, hostmon.ErrRevisionRequired):
		apierr.Write(w, http.StatusPreconditionRequired, "revision_required", err.Error(), nil)
	default:
		writeGateErr(w, err)
	}
}

// hostRead runs one host read, recorded through the gate when there is one.
func (a *API) hostRead(w http.ResponseWriter, r *http.Request, op string, args map[string]any, read func() (any, error)) {
	if a.o.HostReads == nil {
		apierr.Write(w, http.StatusServiceUnavailable, "hosts_unavailable", "this Cerberus serves no host monitor", nil)
		return
	}
	if a.o.Gate == nil {
		out, err := read()
		if err != nil {
			hostErr(w, err)
			return
		}
		writeScoped(GateContext(r.Context()), w, out)
		return
	}
	def, known := hostmon.HostDefinition().Operation(op)
	call, err := a.o.Gate.Begin(GateContext(r.Context()), gate.Spec{Connector: "host", Operation: op, Op: def, Known: known, Config: args})
	if err != nil {
		writeGateErr(w, err)
		return
	}
	out, err := read()
	call.Finish(err)
	if err != nil {
		hostErr(w, err)
		return
	}
	writeScoped(GateContext(r.Context()), w, out)
}

// view is the old HostService.view: declared data, the stored state, and the monitor's status.
func (a *API) view(r *http.Request, h hosts.Host, comps []hosts.Component) HostView {
	v := hostView(h, comps, a.o.Hosts.State(h.Name), time.Now())
	if a.o.HostReads != nil {
		v.Status = a.o.HostReads.HostStatus(r.Context(), h)
	}
	return v
}

func (a *API) hostGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	a.hostRead(w, r, hostmon.OpHostGet, map[string]any{"name": name}, func() (any, error) {
		f, err := a.o.Hosts.Declared()
		if err != nil {
			return nil, err
		}
		for _, h := range f.Hosts {
			if h.Name == name {
				return a.view(r, h, f.Components), nil
			}
		}
		return nil, fmt.Errorf("%w: host %q is not declared; see `cerberus host list` (hosts are declared in hosts.yaml)", hostmon.ErrNotFound, name)
	})
}

// hostProbe measures one host and records its derived state (state/hosts.json in the data
// directory). Observe mode never writes that file: the old build's is read in place.
func (a *API) hostProbe(w http.ResponseWriter, r *http.Request) {
	if a.o.Gate == nil {
		apierr.Write(w, http.StatusConflict, "observe_mode",
			"this Cerberus is in observe mode, so it does not probe hosts: the old Cerberus still owns the host state", nil)
		return
	}
	name := r.PathValue("name")
	a.hostRead(w, r, hostmon.OpHostProbe, map[string]any{"name": name}, func() (any, error) {
		f, err := a.o.Hosts.Declared()
		if err != nil {
			return nil, err
		}
		for _, h := range f.Hosts {
			if h.Name == name {
				if _, err := a.o.HostReads.Prober.Probe(r.Context(), name); err != nil {
					return nil, err
				}
				return a.view(r, h, f.Components), nil
			}
		}
		return nil, fmt.Errorf("%w: host %q is not declared; see `cerberus host list` (hosts are declared in hosts.yaml)", hostmon.ErrNotFound, name)
	})
}

func (a *API) hostStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	a.hostRead(w, r, hostmon.OpHostStatus, map[string]any{"name": name}, func() (any, error) {
		return a.o.HostReads.Status(r.Context(), name)
	})
}

func (a *API) hostStatusAll(w http.ResponseWriter, r *http.Request) {
	a.hostRead(w, r, hostmon.OpHostStatus, map[string]any{}, func() (any, error) {
		return a.o.HostReads.AllStatus(r.Context())
	})
}

func (a *API) hostSamples(w http.ResponseWriter, r *http.Request) {
	name, q := r.PathValue("name"), r.URL.Query()
	now := time.Now()
	since, err := hostmon.ParseSamplesTime(q.Get("since"), now)
	if err != nil {
		hostErr(w, err)
		return
	}
	until, err := hostmon.ParseSamplesTime(q.Get("until"), now)
	if err != nil {
		hostErr(w, err)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	sq := hostmon.SamplesQuery{Since: since, Until: until, Limit: limit, Grain: q.Get("grain")}
	a.hostRead(w, r, hostmon.OpHostSamples, map[string]any{"name": name, "query": sq}, func() (any, error) {
		if sq.Grain != "" && sq.Grain != "raw" {
			out, err := a.o.HostReads.Rollups(r.Context(), name, sq)
			if out == nil && err == nil {
				out = []hosts.Rollup{}
			}
			return out, err
		}
		out, err := a.o.HostReads.Samples(r.Context(), name, sq)
		if out == nil && err == nil {
			out = []hosts.Sample{}
		}
		return out, err
	})
}

func (a *API) hostMonitor(w http.ResponseWriter, r *http.Request) {
	a.hostRead(w, r, hostmon.OpHostMonitor, map[string]any{}, func() (any, error) {
		return a.o.HostReads.MonitorState(r.Context()), nil
	})
}

func (a *API) hostEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	eq := hostmon.EventsQuery{Host: q.Get("host"), Kind: q.Get("kind")}
	eq.AfterID, _ = strconv.ParseInt(q.Get("after_id"), 10, 64)
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		eq.AfterID, _ = strconv.ParseInt(last, 10, 64)
	}
	eq.Limit, _ = strconv.Atoi(q.Get("limit"))
	a.hostRead(w, r, hostmon.OpHostEvents, map[string]any{"query": eq}, func() (any, error) {
		return a.o.HostReads.Events(r.Context(), eq)
	})
}
