package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/target"
)

// Resources is where the read routes get the resources the registry lists, read fresh.
type Resources func() ([]config.ResourceDef, error)

// Observation is what the supervision lane can say about one supervised resource.
type Observation struct {
	// State is a domain.State; empty or "unknown" when nothing is known.
	State string
	// Unit is the systemd unit or launchd label, when there is one.
	Unit string
	// Detail explains an unknown state.
	Detail string
}

// Observer reports a supervised resource's runtime state. In observe mode the supervision
// lane (internal/supervise, slice 3) is the observer; until it is wired, NoObserver reports
// every state as unknown.
type Observer interface {
	Observe(ctx context.Context, r config.ResourceDef) Observation
}

// NoObserver knows nothing about runtime state.
type NoObserver struct{}

// Observe reports unknown.
func (NoObserver) Observe(context.Context, config.ResourceDef) Observation {
	return Observation{State: string(domain.StateUnknown), Detail: "runtime state is not observed by this build yet"}
}

// unsupervisedStatus and the helpers below are the old build's (internal/cerbapi on main):
// a server or container resource is a named handle for connector operations, not a workload.
const unsupervisedStatus = "unsupervised"

func supervisedLocally(resourceType, connector string) bool {
	return resourceType == string(domain.ResourceProcess) && connector == "local"
}

func unsupervisedReason(resourceType, connector string) string {
	return fmt.Sprintf(
		"%s/%s resources are administered through the %s connector, not supervised by the local runtime lane",
		resourceType, connector, connector)
}

func unsupervisedNextStep(id, connector string) string {
	switch connector {
	case "docker":
		return fmt.Sprintf("cerberus docker up %s | cerberus docker down %s | cerberus docker logs %s", id, id, id)
	case "ssh":
		return fmt.Sprintf("cerberus ssh status %s | cerberus ssh exec %s -- <command>", id, id)
	default:
		return fmt.Sprintf("cerberus connectors describe %s", connector)
	}
}

func spec(r config.ResourceDef) (localconn.ProcessSpec, bool) {
	if !supervisedLocally(r.Type, r.Connector) {
		return localconn.ProcessSpec{}, false
	}
	s, err := localconn.SpecFromResourceConfig(r.Config)
	return s, err == nil
}

func resourceMode(r config.ResourceDef) string {
	s, ok := spec(r)
	switch {
	case !ok:
		return ""
	case s.Mode == "":
		return string(localconn.ProcessModeDevSession)
	}
	return string(s.Mode)
}

func resourceSupervisor(r config.ResourceDef) string {
	s, ok := spec(r)
	switch {
	case !ok:
		return ""
	case s.Supervisor == "":
		return string(localconn.ProcessSupervisorAuto)
	}
	return string(s.Supervisor)
}

func resourceRunFrom(r config.ResourceDef) string {
	s, ok := spec(r)
	switch {
	case !ok:
		return ""
	case s.RunFrom == "":
		return string(localconn.ProcessRunFromWorkspace)
	}
	return string(s.RunFrom)
}

func configString(cfg map[string]any, key string) string {
	v, _ := cfg[key].(string)
	return v
}

func configInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func hasBuildStrategy(cfg map[string]any) bool {
	raw, _ := cfg["build_strategy"].(map[string]any)
	kind, _ := raw["kind"].(string)
	return kind != ""
}

func containsTagFold(tags []string, tag string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// ListFilter is the old ResourceListArgs: exact project and connector, case-insensitive tag.
type ListFilter struct {
	ProjectID, Connector, Tag string
}

func (f ListFilter) keep(r config.ResourceDef) bool {
	return (f.ProjectID == "" || r.Project == f.ProjectID) &&
		(f.Connector == "" || r.Connector == f.Connector) &&
		(f.Tag == "" || containsTagFold(r.Tags, f.Tag))
}

// resourceInfo is the old ListResources row for r. The operator-stopped mark and artifact
// drift arrive with supervision (slice 5); in observe mode they are not reported.
func resourceInfo(r config.ResourceDef, obs Observation) ResourceInfo {
	info := ResourceInfo{
		ID: r.ID, Name: r.Name, Type: r.Type, Project: r.Project, Connector: r.Connector, Host: r.Host,
		Mode: resourceMode(r), Supervisor: resourceSupervisor(r), RunFrom: resourceRunFrom(r),
		URL: configString(r.Config, "url"), Port: configInt(r.Config, "port"), HasBuild: hasBuildStrategy(r.Config),
		Tags: append([]string(nil), r.Tags...),
	}
	labels := r.TargetLabels().Resolved()
	info.Env, info.Owner, info.Admin = string(labels.Env), labels.Owner, labels.Admin.String()
	if info.Admin == "" {
		info.Admin = target.AdminUnknown
	}
	if supervisedLocally(r.Type, r.Connector) {
		info.Status = obs.State
	} else {
		info.Status = unsupervisedStatus
		info.RecommendedNextStep = unsupervisedNextStep(r.ID, r.Connector)
	}
	return info
}

// runtimeStatus is the old GetResourceRuntime answer for r, as far as observation goes.
func runtimeStatus(r config.ResourceDef, obs Observation) ResourceRuntimeStatus {
	st := ResourceRuntimeStatus{
		ID: r.ID, Name: r.Name, Type: r.Type, Project: r.Project, Connector: r.Connector,
		URL: configString(r.Config, "url"), Port: configInt(r.Config, "port"),
	}
	if !supervisedLocally(r.Type, r.Connector) {
		st.Status = unsupervisedStatus
		st.RecommendedReason = unsupervisedReason(r.Type, r.Connector)
		st.RecommendedNextStep = unsupervisedNextStep(r.ID, r.Connector)
		return st
	}
	st.Mode, st.Supervisor, st.RunFrom = resourceMode(r), resourceSupervisor(r), resourceRunFrom(r)
	st.HasBuild = hasBuildStrategy(r.Config)
	st.ServiceName = obs.Unit
	st.Status = obs.State
	if st.Status == "" {
		st.Status = string(domain.StateUnknown)
	}
	if obs.Detail != "" {
		st.RecommendedReason = obs.Detail
	}
	return st
}

// listResources returns the filtered resources as old rows, in the resolved config's order
// (as the old list did).
func listResources(ctx context.Context, res Resources, o Observer, f ListFilter) ([]ResourceInfo, error) {
	defs, err := res()
	if err != nil {
		return nil, err
	}
	out := []ResourceInfo{}
	for _, r := range defs {
		if !f.keep(r) {
			continue
		}
		var obs Observation
		if supervisedLocally(r.Type, r.Connector) {
			obs = o.Observe(ctx, r)
		}
		out = append(out, resourceInfo(r, obs))
	}
	return out, nil
}

// findResource returns the resource with id, or errNotFound.
func findResource(res Resources, id string) (config.ResourceDef, error) {
	defs, err := res()
	if err != nil {
		return config.ResourceDef{}, err
	}
	for _, r := range defs {
		if r.ID == id {
			return r, nil
		}
	}
	return config.ResourceDef{}, fmt.Errorf("%w: resource %q not found", errNotFound, id)
}
