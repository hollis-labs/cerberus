package config

// MigrateV1ToV2 converts a v1 Config into a v2 ConfigV2.
// All v1 services become resources with type "process" and connector "local".
func MigrateV1ToV2(v1 *Config) *ConfigV2 {
	v2 := &ConfigV2{Version: 2}

	// Deduplicate projects from service definitions.
	seen := make(map[string]bool)
	for _, svc := range v1.Services {
		if svc.Project != "" && !seen[svc.Project] {
			seen[svc.Project] = true
			v2.Projects = append(v2.Projects, ProjectDef{
				ID:   svc.Project,
				Name: svc.Project,
			})
		}
	}

	// Convert each service to a resource.
	for _, svc := range v1.Services {
		cfg := make(map[string]any)

		if svc.Dir != "" {
			cfg["dir"] = svc.Dir
		}
		if len(svc.Command) > 0 {
			cfg["command"] = svc.Command
		}
		// CRITICAL: omit port 0 — see CLAUDE.md
		if svc.Port != 0 {
			cfg["port"] = svc.Port
		}
		if svc.URL != "" {
			cfg["url"] = svc.URL
		}
		if svc.EnvFile != "" {
			cfg["env_file"] = svc.EnvFile
		}
		if len(svc.Env) > 0 {
			cfg["env"] = svc.Env
		}
		if len(svc.Build) > 0 {
			cfg["build"] = svc.Build
		}
		if svc.Health != "" {
			cfg["health"] = svc.Health
		}
		if svc.HealthCheckCfg.URL != "" || len(svc.HealthCheckCfg.Command) > 0 ||
			svc.HealthCheckCfg.Interval != "" || svc.HealthCheckCfg.Timeout != "" {
			hc := make(map[string]any)
			if svc.HealthCheckCfg.URL != "" {
				hc["url"] = svc.HealthCheckCfg.URL
			}
			if len(svc.HealthCheckCfg.Command) > 0 {
				hc["command"] = svc.HealthCheckCfg.Command
			}
			if svc.HealthCheckCfg.Interval != "" {
				hc["interval"] = svc.HealthCheckCfg.Interval
			}
			if svc.HealthCheckCfg.Timeout != "" {
				hc["timeout"] = svc.HealthCheckCfg.Timeout
			}
			cfg["health_check"] = hc
		}
		if svc.AutoStart {
			cfg["auto_start"] = true
		}
		if svc.AutoRestart {
			cfg["auto_restart"] = true
		}
		if svc.RestartDelay != "" {
			cfg["restart_delay"] = svc.RestartDelay
		}
		if svc.MaxRestartAttempts != 0 {
			cfg["max_restart_attempts"] = svc.MaxRestartAttempts
		}
		if svc.RestartCooldown != "" {
			cfg["restart_cooldown"] = svc.RestartCooldown
		}
		if svc.LogFile != "" {
			cfg["log_file"] = svc.LogFile
		}
		if len(svc.Profiles) > 0 {
			cfg["profiles"] = svc.Profiles
		}
		if svc.Protected {
			cfg["protected"] = true
		}

		r := ResourceDef{
			ID:        svc.ID,
			Name:      svc.Name,
			Type:      "process",
			Project:   svc.Project,
			Connector: "local",
			Config:    cfg,
			Tags:      svc.Tags,
			DependsOn: svc.DependsOn,
		}
		v2.Resources = append(v2.Resources, r)
	}

	return v2
}
