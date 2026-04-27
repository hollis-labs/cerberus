package local

import (
	"time"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/domain"
)

// ServiceDefToResource converts a v1 ServiceDef into a domain Resource.
// All process-specific fields are packed into the Config map.
func ServiceDefToResource(def config.ServiceDef) *domain.Resource {
	spec := ProcessSpec{
		Dir:                def.Dir,
		Command:            append([]string(nil), def.Command...),
		EnvFile:            def.EnvFile,
		Env:                cloneStringMap(def.Env),
		URL:                def.URL,
		Port:               def.Port,
		Build:              append([]string(nil), def.Build...),
		Health:             def.Health,
		HealthCheck:        def.HealthCheckCfg,
		AutoStart:          def.AutoStart,
		AutoRestart:        def.AutoRestart,
		RestartDelay:       def.RestartDelay,
		MaxRestartAttempts: def.MaxRestartAttempts,
		RestartCooldown:    def.RestartCooldown,
		LogFile:            def.LogFile,
		Profiles:           append([]string(nil), def.Profiles...),
		Protected:          def.Protected,
	}

	now := time.Now().UTC()
	return &domain.Resource{
		ID:        def.ID,
		Name:      def.Name,
		Type:      domain.ResourceProcess,
		ProjectID: def.Project,
		Connector: "local",
		Config:    spec.ToResourceConfig(),
		Tags:      def.Tags,
		DependsOn: def.DependsOn,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// ResourceToServiceDef converts a domain Resource back to a v1 ServiceDef.
// This is used by the local connector to interface with the existing
// service.ManagedService code.
func ResourceToServiceDef(res *domain.Resource) config.ServiceDef {
	spec, _ := SpecFromResourceConfig(res.Config)

	def := config.ServiceDef{
		ID:      res.ID,
		Name:    res.Name,
		Project: res.ProjectID,
		Dir:     spec.Dir,
		Command: append([]string(nil), spec.Command...),
		EnvFile: spec.EnvFile,
		Env:     cloneStringMap(spec.Env),
		URL:     spec.URL,
		Port:    spec.Port,
		Tags:    res.Tags,
		Build:   append([]string(nil), spec.Build...),
		Health:  spec.Health,
		HealthCheckCfg: config.HealthCheck{
			URL:      spec.HealthCheck.URL,
			Command:  append([]string(nil), spec.HealthCheck.Command...),
			Interval: spec.HealthCheck.Interval,
			Timeout:  spec.HealthCheck.Timeout,
		},
		AutoStart:          spec.AutoStart,
		AutoRestart:        spec.AutoRestart,
		RestartDelay:       spec.RestartDelay,
		MaxRestartAttempts: spec.MaxRestartAttempts,
		RestartCooldown:    spec.RestartCooldown,
		LogFile:            spec.LogFile,
		Profiles:           append([]string(nil), spec.Profiles...),
		Protected:          spec.Protected,
	}

	// Also handle DependsOn from the resource level
	def.DependsOn = res.DependsOn

	return def
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
