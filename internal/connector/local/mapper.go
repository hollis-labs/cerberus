package local

import (
	"time"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/domain"
)

// ServiceDefToResource converts a v1 ServiceDef into a domain Resource.
// All process-specific fields are packed into the Config map.
func ServiceDefToResource(def config.ServiceDef) *domain.Resource {
	cfg := make(map[string]any)

	// Required fields
	if def.Dir != "" {
		cfg["dir"] = def.Dir
	}
	if len(def.Command) > 0 {
		cfg["command"] = def.Command
	}

	// Optional fields — only include non-zero values
	if def.EnvFile != "" {
		cfg["env_file"] = def.EnvFile
	}
	if len(def.Env) > 0 {
		cfg["env"] = def.Env
	}
	if def.URL != "" {
		cfg["url"] = def.URL
	}
	// CRITICAL: never include port 0 — see CLAUDE.md
	if def.Port > 0 {
		cfg["port"] = def.Port
	}
	if len(def.Build) > 0 {
		cfg["build"] = def.Build
	}
	if def.Health != "" {
		cfg["health"] = def.Health
	}
	if def.HealthCheckCfg.URL != "" || len(def.HealthCheckCfg.Command) > 0 {
		hc := make(map[string]any)
		if def.HealthCheckCfg.URL != "" {
			hc["url"] = def.HealthCheckCfg.URL
		}
		if len(def.HealthCheckCfg.Command) > 0 {
			hc["command"] = def.HealthCheckCfg.Command
		}
		if def.HealthCheckCfg.Interval != "" {
			hc["interval"] = def.HealthCheckCfg.Interval
		}
		if def.HealthCheckCfg.Timeout != "" {
			hc["timeout"] = def.HealthCheckCfg.Timeout
		}
		cfg["health_check"] = hc
	}
	if def.AutoStart {
		cfg["auto_start"] = true
	}
	if def.AutoRestart {
		cfg["auto_restart"] = true
	}
	if def.RestartDelay != "" {
		cfg["restart_delay"] = def.RestartDelay
	}
	if def.MaxRestartAttempts > 0 {
		cfg["max_restart_attempts"] = def.MaxRestartAttempts
	}
	if def.RestartCooldown != "" {
		cfg["restart_cooldown"] = def.RestartCooldown
	}
	if def.LogFile != "" {
		cfg["log_file"] = def.LogFile
	}
	if len(def.Profiles) > 0 {
		cfg["profiles"] = def.Profiles
	}
	if def.Protected {
		cfg["protected"] = true
	}

	now := time.Now().UTC()
	return &domain.Resource{
		ID:        def.ID,
		Name:      def.Name,
		Type:      domain.ResourceProcess,
		ProjectID: def.Project,
		Connector: "local",
		Config:    cfg,
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
	def := config.ServiceDef{
		ID:      res.ID,
		Name:    res.Name,
		Project: res.ProjectID,
		Tags:    res.Tags,
	}

	c := res.Config

	if v, ok := c["dir"].(string); ok {
		def.Dir = v
	}
	switch v := c["command"].(type) {
	case []any:
		def.Command = toStringSlice(v)
	case []string:
		def.Command = v
	}
	if v, ok := c["env_file"].(string); ok {
		def.EnvFile = v
	}
	switch v := c["env"].(type) {
	case map[string]any:
		def.Env = toStringMap(v)
	case map[string]string:
		def.Env = v
	}
	if v, ok := c["url"].(string); ok {
		def.URL = v
	}
	if v, ok := c["port"]; ok {
		def.Port = toInt(v)
	}
	switch v := c["build"].(type) {
	case []any:
		def.Build = toStringSlice(v)
	case []string:
		def.Build = v
	}
	if v, ok := c["health"].(string); ok {
		def.Health = v
	}
	if v, ok := c["health_check"].(map[string]any); ok {
		if u, ok := v["url"].(string); ok {
			def.HealthCheckCfg.URL = u
		}
		switch cmd := v["command"].(type) {
		case []any:
			def.HealthCheckCfg.Command = toStringSlice(cmd)
		case []string:
			def.HealthCheckCfg.Command = cmd
		}
		if i, ok := v["interval"].(string); ok {
			def.HealthCheckCfg.Interval = i
		}
		if t, ok := v["timeout"].(string); ok {
			def.HealthCheckCfg.Timeout = t
		}
	}
	if v, ok := c["auto_start"].(bool); ok {
		def.AutoStart = v
	}
	if v, ok := c["auto_restart"].(bool); ok {
		def.AutoRestart = v
	}
	if v, ok := c["restart_delay"].(string); ok {
		def.RestartDelay = v
	}
	if v, ok := c["max_restart_attempts"]; ok {
		def.MaxRestartAttempts = toInt(v)
	}
	if v, ok := c["restart_cooldown"].(string); ok {
		def.RestartCooldown = v
	}
	if v, ok := c["log_file"].(string); ok {
		def.LogFile = v
	}
	switch v := c["profiles"].(type) {
	case []any:
		def.Profiles = toStringSlice(v)
	case []string:
		def.Profiles = v
	}
	if v, ok := c["protected"].(bool); ok {
		def.Protected = v
	}

	// Also handle DependsOn from the resource level
	def.DependsOn = res.DependsOn

	return def
}

func toStringSlice(v []any) []string {
	out := make([]string, 0, len(v))
	for _, item := range v {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func toStringMap(v map[string]any) map[string]string {
	out := make(map[string]string, len(v))
	for k, val := range v {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	default:
		return 0
	}
}
