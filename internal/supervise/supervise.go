// Package supervise is the Substrate build's supervision lane (docs/substrate-port-plan.md
// sections 4.2 and 7.2). In observe mode it reads the descriptors the registry lists and the
// state of each workload, reports status, and refuses every mutation: the old build still owns
// the units, and two writers must never meet. In supervise mode it runs the old build's
// mutations (apply, deploy, reload, stop, sync, remove) with the old build's results.
//
// Observe writes nothing anywhere. A systemd unit's state comes from `systemctl --user show`;
// a dev session's from this build's pid and launch metadata files in the data directory, read
// and never tidied (a stale pid file is reported, not removed).
package supervise

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/settings"
)

// Verbs are the mutations the supervision lane serves in supervise mode, the old build's six.
// Observe mode refuses each of them. There is no start: apply starts (plan 7.2 D7).
var Verbs = []string{"apply", "deploy", "stop", "reload", "sync", "remove"}

// Options build a Service.
type Options struct {
	Mode settings.Mode
	// Resources returns the resources the registry lists, read fresh.
	Resources func() ([]config.ResourceDef, error)
	// Home is the home directory units and install layouts derive from.
	Home string
	// StateDir is the data directory, where this build's dev session pid files live. It is never
	// the old build's tree: new never reads ~/.cerberus/pids or locks (plan section 3).
	StateDir string
	// Runner runs systemctl for unit state; nil is the user manager.
	Runner localconn.CommandRunner
	// SelfID is the resource this process runs as; empty when it has none.
	SelfID string

	// Supervise mode only.

	// Config returns the configuration the registry assembles, read fresh for each call.
	Config func() (*config.ConfigV2, error)
	// InstallAfterBuild is the global install-after-build default a deploy uses below the
	// resource's own install_after_build and the caller's override: cerberus.build.install_after_build
	// (settings). Nil is the old build's default, true. The registry's assembled config is not
	// consulted for it: the global default is the operator's setting, not a project's.
	InstallAfterBuild *bool
	// Store is the class A store: pause state and applied digests.
	Store *store.Store
	// Digester keys applied digests (the audit chain's digest key).
	Digester Digester
	// Gate is the operation gate every mutation passes (policy, approvals, brakes, audit,
	// egress), and the monitor records its restarts on and skips a frozen resource by. There is
	// no supervise mode without one: New refuses it.
	Gate *gate.Gate
	// Logger receives the lane's records; nil is slog.Default.
	Logger *slog.Logger
	// Systemd configures the connector's systemd backend. Runner defaults to Options.Runner and
	// Env.Home to Options.Home; tests set a fake systemctl here.
	Systemd localconn.SystemdOptions
}

// Service is the supervision lane.
type Service struct {
	o Options
	// opMu serializes mutations, as the old build's runtime service did.
	opMu sync.Mutex
	conn *localconn.Connector

	monMu sync.Mutex
	mon   *monitor
}

// New builds the lane.
func New(o Options) (*Service, error) {
	if o.Config != nil && o.Resources == nil {
		o.Resources = func() ([]config.ResourceDef, error) {
			cfg, err := o.Config()
			if err != nil || cfg == nil {
				return nil, err
			}
			return cfg.Resources, nil
		}
	}
	switch o.Mode {
	case settings.ModeObserve:
	case settings.ModeSupervise:
		switch {
		case o.Config == nil:
			return nil, errors.New("supervise: supervise mode needs a config source")
		case o.Store == nil:
			return nil, errors.New("supervise: supervise mode needs the class A store")
		case o.Digester == nil:
			return nil, errors.New("supervise: supervise mode needs the applied-digest key")
		case o.Gate == nil:
			return nil, errors.New("supervise: supervise mode needs the operation gate; nothing mutates ungated")
		}
	default:
		return nil, fmt.Errorf("unknown supervision mode %q", o.Mode)
	}
	if o.Resources == nil {
		return nil, errors.New("supervise: a resource source is required")
	}
	if o.Runner == nil {
		o.Runner = localconn.UserBusRunner()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Systemd.Runner == nil {
		o.Systemd.Runner = o.Runner
	}
	if o.Systemd.Env.Home == "" {
		o.Systemd.Env.Home = o.Home
	}
	s := &Service{o: o, conn: localconn.NewWithSystemd(o.Systemd)}
	// Every local mutation, including one the lane does not start itself, refuses the
	// resource this process runs as.
	s.conn.SetMutationGuard(func(res *domain.Resource, _ localconn.ProcessSpec) error {
		return s.refuseSelfID(res.ID)
	})
	return s, nil
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
	// Systemd is the unit's show record for a systemd-supervised resource whose unit the
	// user manager answered for; nil otherwise. Not serialized: the API maps it onto the old
	// systemd_* fields.
	Systemd *localconn.SystemdRecord `json:"-"`
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

// observeRefusal is every mutation's answer in observe mode: the old build still supervises.
func (s *Service) observeRefusal(verb, id string) error {
	return &Refusal{Status: http.StatusConflict, Code: "observe_mode", Message: fmt.Sprintf(
		"this Cerberus is in observe mode, so it refused %s of %q: the old Cerberus still supervises the workloads and runs every change. "+
			"Run the command with the old CLI until the handover (docs/substrate-port-plan.md section 7.2 D)", verb, id)}
}

// IsObserveRefusal reports whether err is observe mode's refusal.
func IsObserveRefusal(err error) bool {
	var r *Refusal
	return errors.As(err, &r) && r.Code == "observe_mode"
}

// refuseSelf is the self-guard (plan section 4.2): no mutation through this process's own API
// reaches the resource it runs as. The old CLI or fleet-install changes it, never itself.
func (s *Service) refuseSelf(res *config.ResourceDef) error { return s.refuseSelfID(res.ID) }

func (s *Service) refuseSelfID(id string) error {
	if s.o.SelfID == "" || id != s.o.SelfID {
		return nil
	}
	return redact.Guidance("refusing to mutate %q: it is the resource this Cerberus runs as (cerberus.self_resource_id), "+
		"and a Cerberus never changes itself through its own API. Upgrade it with fleet-install, or change it from another Cerberus", id)
}

// Observe observes one resource the caller already holds, without reading the registry.
func (s *Service) Observe(ctx context.Context, res *config.ResourceDef) Status {
	return s.observe(ctx, res)
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
		state, unit, rec, err := localconn.ObserveSystemdDetail(ctx, s.o.Runner, s.o.Home, dr)
		st.State, st.Unit = state, unit
		if rec.LoadState != "" {
			st.Systemd = &rec
		}
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
