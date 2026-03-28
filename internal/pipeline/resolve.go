package pipeline

import (
	"fmt"
	"time"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/pipeline/actions"
	"github.com/chrispian/cerberus/internal/service"
)

// Resolve converts a config PipelineDef into an executable Pipeline by
// looking up resources and creating the appropriate action instances.
func Resolve(def config.PipelineDef, services []*service.ManagedService, local *localconn.Connector) (*Pipeline, error) {
	b := New(def.ID).Name(def.Name).Description(def.Description)

	for _, sd := range def.Stages {
		sb := b.Stage(sd.Name)
		if len(sd.DependsOn) > 0 {
			sb.DependsOn(sd.DependsOn...)
		}

		for _, ad := range sd.Actions {
			action, err := resolveAction(ad, services, local)
			if err != nil {
				return nil, fmt.Errorf("pipeline %q, stage %q: %w", def.ID, sd.Name, err)
			}
			sb.Action(action)
		}
		sb.Done()
	}

	return b.Build()
}

func resolveAction(ad config.ActionDef, services []*service.ManagedService, local *localconn.Connector) (domain.Action, error) {
	switch ad.Type {
	case "build":
		svc := findService(services, ad.Resource)
		if svc == nil {
			return nil, fmt.Errorf("build action: resource %q not found", ad.Resource)
		}
		return actions.NewBuild(ad.Resource, svc), nil

	case "start":
		svc := findService(services, ad.Resource)
		if svc == nil {
			return nil, fmt.Errorf("start action: resource %q not found", ad.Resource)
		}
		res := localconn.ServiceDefToResource(svc.Def)
		return actions.NewStart(ad.Resource, local, res), nil

	case "stop":
		svc := findService(services, ad.Resource)
		if svc == nil {
			return nil, fmt.Errorf("stop action: resource %q not found", ad.Resource)
		}
		res := localconn.ServiceDefToResource(svc.Def)
		return actions.NewStop(ad.Resource, local, res), nil

	case "health_wait":
		svc := findService(services, ad.Resource)
		if svc == nil {
			return nil, fmt.Errorf("health_wait action: resource %q not found", ad.Resource)
		}
		url := svc.Def.HealthCheckCfg.URL
		if url == "" {
			url = svc.Def.Health
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

func findService(services []*service.ManagedService, id string) *service.ManagedService {
	for _, svc := range services {
		if svc.Def.ID == id {
			return svc
		}
	}
	return nil
}
