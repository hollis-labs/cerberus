// Package supervise is the Substrate build's supervision lane (docs/substrate-port-plan.md
// sections 4.2 and 7.2 C). In observe mode, the only mode this slice enables, it reads the
// descriptors the registry lists and the state of each workload, reports status, and refuses
// every mutation: the old build still owns the units, and two writers must never meet.
//
// Observe writes nothing anywhere. A systemd unit's state comes from `systemctl --user show`;
// a dev session's from the old build's pid and launch metadata files, read in place and never
// tidied (a stale pid file is reported, not removed).
package supervise

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/settings"
)

// Verbs are the mutations the supervision lane will serve in supervise mode. Observe mode
// refuses each of them.
var Verbs = []string{"apply", "deploy", "start", "stop", "reload", "sync", "remove"}

// ErrObserve is the class of every refusal in observe mode.
var ErrObserve = errors.New("observe mode")

// Options build a Service.
type Options struct {
	Mode settings.Mode
	// Resources returns the resources the registry lists, read fresh.
	Resources func() ([]config.ResourceDef, error)
	// Home is the home directory units and install layouts derive from.
	Home string
	// StateDir is the old build's state tree (adopt_root), where dev session pid files live.
	StateDir string
	// Runner runs systemctl for unit state; nil is the user manager.
	Runner localconn.CommandRunner
	// SelfID is the resource this process runs as; empty when it has none.
	SelfID string
}

// Service is the supervision lane.
type Service struct {
	o Options
}

// New builds the lane. Supervise mode is refused until slice 5 enables it.
func New(o Options) (*Service, error) {
	switch o.Mode {
	case settings.ModeObserve:
	case settings.ModeSupervise:
		return nil, redact.Guidance("cerberus.mode supervise is not available in this build yet (it arrives with slice 5 of the port); set cerberus.mode: observe")
	default:
		return nil, fmt.Errorf("unknown supervision mode %q", o.Mode)
	}
	if o.Resources == nil {
		return nil, errors.New("supervise: a resource source is required")
	}
	if o.Runner == nil {
		o.Runner = localconn.UserBusRunner()
	}
	return &Service{o: o}, nil
}

// Mode is the lane's mode.
func (s *Service) Mode() settings.Mode { return s.o.Mode }

// Status is one resource as observed.
type Status struct {
	ID         string       `json:"id"`
	Project    string       `json:"project,omitempty"`
	Type       string       `json:"type"`
	Connector  string       `json:"connector"`
	Mode       string       `json:"mode,omitempty"`
	Supervisor string       `json:"supervisor,omitempty"`
	Unit       string       `json:"unit,omitempty"`
	State      domain.State `json:"state"`
	Detail     string       `json:"detail,omitempty"`
	// Self marks the resource this process runs as.
	Self bool `json:"self,omitempty"`
	// Observed is false for a resource the lane does not supervise (a named handle for
	// connector operations, such as an ssh server or a container).
	Observed bool `json:"observed"`
}

// List observes every resource the registry lists, sorted by id.
func (s *Service) List(ctx context.Context) ([]Status, error) {
	resources, err := s.o.Resources()
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(resources))
	for i := range resources {
		out = append(out, s.observe(ctx, &resources[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Status observes one resource.
func (s *Service) Status(ctx context.Context, id string) (Status, error) {
	resources, err := s.o.Resources()
	if err != nil {
		return Status{}, err
	}
	for i := range resources {
		if resources[i].ID == id {
			return s.observe(ctx, &resources[i]), nil
		}
	}
	return Status{}, fmt.Errorf("resource %q not found", id)
}

// Mutate refuses every verb in observe mode.
func (s *Service) Mutate(_ context.Context, verb, id string) error {
	known := false
	for _, v := range Verbs {
		known = known || v == verb
	}
	if !known {
		return fmt.Errorf("unknown resource verb %q", verb)
	}
	return fmt.Errorf("%w: %w", ErrObserve, redact.Guidance(
		"this Cerberus is in observe mode, so it refused %s of %q: the old Cerberus still supervises the workloads and runs every change. "+
			"Run the command with the old CLI until the handover (docs/substrate-port-plan.md section 7.2 D)", verb, id))
}

func (s *Service) observe(ctx context.Context, res *config.ResourceDef) Status {
	st := Status{ID: res.ID, Project: res.Project, Type: res.Type, Connector: res.Connector, Self: res.ID == s.o.SelfID}
	if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
		st.State, st.Detail = domain.StateUnknown, "not a local process: a named handle for connector operations, unsupervised"
		return st
	}
	spec, err := localconn.SpecFromResourceConfig(res.Config)
	if err != nil {
		st.State, st.Detail = domain.StateUnknown, err.Error()
		return st
	}
	st.Observed = true
	st.Mode, st.Supervisor = string(spec.Mode), string(spec.Supervisor)
	dr := ResourceToDomain(res)
	switch {
	case spec.Mode == "" || spec.Mode == localconn.ProcessModeDevSession:
		st.Mode = string(localconn.ProcessModeDevSession)
		st.State, st.Detail = localconn.ObserveDevSession(s.o.StateDir, res.ID)
	case localconn.IsSystemdSupervised(spec):
		state, unit, err := localconn.ObserveSystemdUnit(ctx, s.o.Runner, s.o.Home, dr)
		st.State, st.Unit = state, unit
		if err != nil {
			st.Detail = err.Error()
		}
	default:
		st.State, st.Detail = domain.StateUnknown, fmt.Sprintf("supervisor %q is not observed on this host", spec.Supervisor)
	}
	return st
}

// ResourceToDomain is the connector's view of a registry resource.
func ResourceToDomain(r *config.ResourceDef) *domain.Resource {
	return &domain.Resource{
		ID:        r.ID,
		Name:      r.Name,
		Type:      domain.ResourceType(r.Type),
		ProjectID: r.Project,
		Connector: r.Connector,
		Config:    r.Config,
		Tags:      append([]string(nil), r.Tags...),
		DependsOn: append([]string(nil), r.DependsOn...),
	}
}
