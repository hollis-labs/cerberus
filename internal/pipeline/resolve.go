package pipeline

import (
	"fmt"
	"time"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/pipeline/actions"
)

// Resolve converts a config PipelineDef into an executable Pipeline by
// looking up resources and creating the appropriate action instances.
func Resolve(def config.PipelineDef, resources []config.ResourceDef, local *localconn.Connector) (*Pipeline, error) {
	b := New(def.ID).Name(def.Name).Description(def.Description)

	for _, sd := range def.Stages {
		sb := b.Stage(sd.Name)
		if len(sd.DependsOn) > 0 {
			sb.DependsOn(sd.DependsOn...)
		}

		for _, ad := range sd.Actions {
			action, err := resolveAction(ad, resources, local)
			if err != nil {
				return nil, fmt.Errorf("pipeline %q, stage %q: %w", def.ID, sd.Name, err)
			}
			sb.Action(action)
		}
		sb.Done()
	}

	return b.Build()
}

func resolveAction(ad config.ActionDef, resources []config.ResourceDef, local *localconn.Connector) (domain.Action, error) {
	switch ad.Type {
	case "build", "build_app":
		res := findResource(resources, ad.Resource)
		if res == nil {
			return nil, fmt.Errorf("build action: resource %q not found", ad.Resource)
		}
		spec, err := requireLocalProcessSpec(*res)
		if err != nil {
			return nil, fmt.Errorf("build action: %w", err)
		}
		return actions.NewBuild(ad.Resource, spec), nil

	case "deploy", "deploy_app":
		res := findResource(resources, ad.Resource)
		if res == nil {
			return nil, fmt.Errorf("deploy action: resource %q not found", ad.Resource)
		}
		spec, err := requireLocalProcessSpec(*res)
		if err != nil {
			return nil, fmt.Errorf("deploy action: %w", err)
		}
		return actions.NewDeploy(ad.Resource, resourceDefToDomain(*res), spec, local), nil

	case "start":
		res := findResource(resources, ad.Resource)
		if res == nil {
			return nil, fmt.Errorf("start action: resource %q not found", ad.Resource)
		}
		return actions.NewStart(ad.Resource, local, resourceDefToDomain(*res)), nil

	case "stop":
		res := findResource(resources, ad.Resource)
		if res == nil {
			return nil, fmt.Errorf("stop action: resource %q not found", ad.Resource)
		}
		return actions.NewStop(ad.Resource, local, resourceDefToDomain(*res)), nil

	case "health_wait":
		res := findResource(resources, ad.Resource)
		if res == nil {
			return nil, fmt.Errorf("health_wait action: resource %q not found", ad.Resource)
		}
		spec, err := requireLocalProcessSpec(*res)
		if err != nil {
			return nil, fmt.Errorf("health_wait action: %w", err)
		}
		url := spec.HealthCheck.URL
		if url == "" {
			url = spec.Health
		}
		if url == "" {
			return nil, fmt.Errorf("health_wait action: resource %q has no health check URL", ad.Resource)
		}
		timeout := 30 * time.Second
		if ad.Timeout != "" {
			d, err := time.ParseDuration(ad.Timeout)
			if err != nil {
				return nil, fmt.Errorf("health_wait action: invalid timeout %q: %w", ad.Timeout, err)
			}
			timeout = d
		}
		return actions.NewHealthWait(ad.Resource, url, timeout), nil

	case "shell":
		if ad.Command == "" {
			return nil, fmt.Errorf("shell action: command is required")
		}
		name := "shell"
		if ad.Resource != "" {
			name = fmt.Sprintf("shell(%s)", ad.Resource)
		}
		return actions.NewShell(name, ad.Command, ad.Dir), nil

	default:
		return nil, fmt.Errorf("unknown action type %q", ad.Type)
	}
}

func findResource(resources []config.ResourceDef, id string) *config.ResourceDef {
	for i := range resources {
		if resources[i].ID == id {
			return &resources[i]
		}
	}
	return nil
}

func resourceDefToDomain(r config.ResourceDef) *domain.Resource {
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

func requireLocalProcessSpec(r config.ResourceDef) (localconn.ProcessSpec, error) {
	if r.Type != string(domain.ResourceProcess) || r.Connector != "local" {
		return localconn.ProcessSpec{}, fmt.Errorf("resource %q is %s/%s; local process action required", r.ID, r.Type, r.Connector)
	}
	spec, err := localconn.SpecFromResourceConfig(r.Config)
	if err != nil {
		return localconn.ProcessSpec{}, fmt.Errorf("decode process spec for %q: %w", r.ID, err)
	}
	return spec, nil
}
