package local

import (
	"fmt"

	"github.com/chrispian/cerberus/internal/config"
)

// ProcessMode distinguishes the local runtime strategy for a process resource.
type ProcessMode string

const (
	ProcessModeDevSession ProcessMode = "dev_session"
	ProcessModeOSService  ProcessMode = "os_service"
)

// ProcessSupervisor selects the native supervisor for durable services.
type ProcessSupervisor string

const (
	ProcessSupervisorAuto           ProcessSupervisor = "auto"
	ProcessSupervisorLaunchd        ProcessSupervisor = "launchd"
	ProcessSupervisorSystemdUser    ProcessSupervisor = "systemd_user"
	ProcessSupervisorWindowsService ProcessSupervisor = "windows_service"
)

// ProcessRunFrom indicates whether the process should run directly from the
// workspace or from an installed artifact in the Cerberus user area.
type ProcessRunFrom string

const (
	ProcessRunFromWorkspace ProcessRunFrom = "workspace"
	ProcessRunFromArtifact  ProcessRunFrom = "artifact"
)

// ProcessSpec is the typed local-process view of a resource config.
//
// It preserves the existing v1-compatible service fields while adding the
// explicit daemon-management fields needed for v2 service backends.
type ProcessSpec struct {
	Dir                string
	Command            []string
	EnvFile            string
	Env                map[string]string
	URL                string
	Port               int
	BuildStrategy      *BuildStrategyConfig
	Health             string
	HealthCheck        config.HealthCheck
	AutoStart          bool
	AutoRestart        bool
	RestartDelay       string
	MaxRestartAttempts int
	RestartCooldown    string
	LogFile            string
	Profiles           []string
	Protected          bool
	Mode               ProcessMode
	Supervisor         ProcessSupervisor
	RunFrom            ProcessRunFrom
	ServiceName        string
	ArtifactPath       string
	InstallRoot        string
	InstallWorkDir     string
	// InstallAfterBuild reflects the resource-level value parsed from the
	// `install_after_build` config key. The field is plain bool and defaults
	// to true when the YAML key is absent — that matches the documented
	// default-on behavior and keeps ToResourceConfig round-trips clean
	// (an implicit-default resource doesn't get re-emitted as an explicit
	// opt-out). Callers that need to distinguish "absent" from "explicit
	// false" should still probe presence on the raw config map and route
	// through cerbapi.ResolveInstallAfterBuild.
	InstallAfterBuild bool
}

// SpecFromResourceConfig decodes a local process config map into a typed spec.
func SpecFromResourceConfig(cfg map[string]any) (ProcessSpec, error) {
	spec := ProcessSpec{
		Mode:       ProcessModeDevSession,
		Supervisor: ProcessSupervisorAuto,
		RunFrom:    ProcessRunFromWorkspace,
	}

	var ok bool

	if spec.Dir, ok = stringField(cfg, "dir"); !ok {
		spec.Dir = ""
	}
	spec.Command = stringSliceField(cfg, "command")
	spec.EnvFile, _ = stringField(cfg, "env_file")
	spec.Env = stringMapField(cfg, "env")
	spec.URL, _ = stringField(cfg, "url")
	spec.Port = intField(cfg, "port")
	// `build:` is deprecated in favor of `build_strategy:`. Rather than
	// hard-rejecting it (which silently drops every not-yet-migrated
	// resource out of the runtime), translate a legacy command list into
	// an equivalent legacy_command build_strategy so existing apps keep
	// working. The deprecation is surfaced by ValidateProjectConfig /
	// `cerberus registry health` to nudge migration to a first-class
	// strategy.
	_, hasLegacyBuild := cfg["build"]
	_, hasStrategy := cfg["build_strategy"]
	switch {
	case hasLegacyBuild && hasStrategy:
		return ProcessSpec{}, fmt.Errorf("config sets both build (deprecated) and build_strategy; remove the legacy build field")
	case hasLegacyBuild:
		command := stringSliceField(cfg, "build")
		if len(command) == 0 {
			return ProcessSpec{}, fmt.Errorf("build is deprecated and could not be translated: expected a non-empty command list")
		}
		spec.BuildStrategy = &BuildStrategyConfig{
			Kind:  LegacyCommandKind,
			Rules: map[string]any{"command": command},
		}
	case hasStrategy:
		strategy, err := buildStrategyConfigFromAny(cfg["build_strategy"])
		if err != nil {
			return ProcessSpec{}, fmt.Errorf("decode build_strategy: %w", err)
		}
		spec.BuildStrategy = strategy
	}
	spec.InstallAfterBuild = boolFieldDefault(cfg, "install_after_build", true)
	spec.Health, _ = stringField(cfg, "health")
	spec.AutoStart = boolField(cfg, "auto_start")
	spec.AutoRestart = boolField(cfg, "auto_restart")
	spec.RestartDelay, _ = stringField(cfg, "restart_delay")
	spec.MaxRestartAttempts = intField(cfg, "max_restart_attempts")
	spec.RestartCooldown, _ = stringField(cfg, "restart_cooldown")
	spec.LogFile, _ = stringField(cfg, "log_file")
	spec.Profiles = stringSliceField(cfg, "profiles")
	spec.Protected = boolField(cfg, "protected")

	if hcRaw, exists := cfg["health_check"]; exists {
		hc, err := healthCheckFromAny(hcRaw)
		if err != nil {
			return ProcessSpec{}, fmt.Errorf("decode health_check: %w", err)
		}
		spec.HealthCheck = hc
	}

	if raw, ok := stringField(cfg, "mode"); ok && raw != "" {
		spec.Mode = ProcessMode(raw)
	}
	if raw, ok := stringField(cfg, "supervisor"); ok && raw != "" {
		spec.Supervisor = ProcessSupervisor(raw)
	}
	if raw, ok := stringField(cfg, "run_from"); ok && raw != "" {
		spec.RunFrom = ProcessRunFrom(raw)
	}
	spec.ServiceName, _ = stringField(cfg, "service_name")
	spec.ArtifactPath, _ = stringField(cfg, "artifact_path")
	spec.InstallRoot, _ = stringField(cfg, "install_root")
	spec.InstallWorkDir, _ = stringField(cfg, "install_work_dir")
	spec.Dir = config.ExpandHomePath(spec.Dir)
	spec.Command = expandHomeSlice(spec.Command)
	spec.EnvFile = config.ExpandHomePath(spec.EnvFile)
	spec.Env = expandHomeMapValues(spec.Env)
	if spec.BuildStrategy != nil {
		spec.BuildStrategy.expandHome()
	}
	spec.LogFile = config.ExpandHomePath(spec.LogFile)
	spec.ArtifactPath = config.ExpandHomePath(spec.ArtifactPath)
	spec.InstallRoot = config.ExpandHomePath(spec.InstallRoot)
	spec.InstallWorkDir = config.ExpandHomePath(spec.InstallWorkDir)
	spec.HealthCheck.Command = expandHomeSlice(spec.HealthCheck.Command)

	return spec, nil
}

// ToResourceConfig re-encodes the typed process spec into a resource config
// map, omitting zero-value fields.
func (s ProcessSpec) ToResourceConfig() map[string]any {
	cfg := make(map[string]any)

	if s.Dir != "" {
		cfg["dir"] = s.Dir
	}
	if len(s.Command) > 0 {
		cfg["command"] = append([]string(nil), s.Command...)
	}
	if s.EnvFile != "" {
		cfg["env_file"] = s.EnvFile
	}
	if len(s.Env) > 0 {
		env := make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			env[k] = v
		}
		cfg["env"] = env
	}
	if s.URL != "" {
		cfg["url"] = s.URL
	}
	if s.Port > 0 {
		cfg["port"] = s.Port
	}
	if s.BuildStrategy != nil && s.BuildStrategy.Kind != "" {
		cfg["build_strategy"] = s.BuildStrategy.toConfigMap()
	}
	if !s.InstallAfterBuild {
		// Only emit the explicit opt-out; the default is true, so emitting
		// `true` would just churn YAML files for no semantic gain.
		cfg["install_after_build"] = false
	}
	if s.Health != "" {
		cfg["health"] = s.Health
	}
	if s.HealthCheck.URL != "" || len(s.HealthCheck.Command) > 0 ||
		s.HealthCheck.Interval != "" || s.HealthCheck.Timeout != "" {
		hc := make(map[string]any)
		if s.HealthCheck.URL != "" {
			hc["url"] = s.HealthCheck.URL
		}
		if len(s.HealthCheck.Command) > 0 {
			hc["command"] = append([]string(nil), s.HealthCheck.Command...)
		}
		if s.HealthCheck.Interval != "" {
			hc["interval"] = s.HealthCheck.Interval
		}
		if s.HealthCheck.Timeout != "" {
			hc["timeout"] = s.HealthCheck.Timeout
		}
		cfg["health_check"] = hc
	}
	if s.AutoStart {
		cfg["auto_start"] = true
	}
	if s.AutoRestart {
		cfg["auto_restart"] = true
	}
	if s.RestartDelay != "" {
		cfg["restart_delay"] = s.RestartDelay
	}
	if s.MaxRestartAttempts > 0 {
		cfg["max_restart_attempts"] = s.MaxRestartAttempts
	}
	if s.RestartCooldown != "" {
		cfg["restart_cooldown"] = s.RestartCooldown
	}
	if s.LogFile != "" {
		cfg["log_file"] = s.LogFile
	}
	if len(s.Profiles) > 0 {
		cfg["profiles"] = append([]string(nil), s.Profiles...)
	}
	if s.Protected {
		cfg["protected"] = true
	}
	if s.Mode != "" && s.Mode != ProcessModeDevSession {
		cfg["mode"] = string(s.Mode)
	}
	if s.Supervisor != "" && s.Supervisor != ProcessSupervisorAuto {
		cfg["supervisor"] = string(s.Supervisor)
	}
	if s.RunFrom != "" && s.RunFrom != ProcessRunFromWorkspace {
		cfg["run_from"] = string(s.RunFrom)
	}
	if s.ServiceName != "" {
		cfg["service_name"] = s.ServiceName
	}
	if s.ArtifactPath != "" {
		cfg["artifact_path"] = s.ArtifactPath
	}
	if s.InstallRoot != "" {
		cfg["install_root"] = s.InstallRoot
	}
	if s.InstallWorkDir != "" {
		cfg["install_work_dir"] = s.InstallWorkDir
	}

	return cfg
}

func stringField(cfg map[string]any, key string) (string, bool) {
	v, ok := cfg[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func stringSliceField(cfg map[string]any, key string) []string {
	v, ok := cfg[key]
	if !ok {
		return nil
	}
	switch xs := v.(type) {
	case []string:
		return append([]string(nil), xs...)
	case []any:
		out := make([]string, 0, len(xs))
		for _, item := range xs {
			s, ok := item.(string)
			if ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func stringMapField(cfg map[string]any, key string) map[string]string {
	v, ok := cfg[key]
	if !ok {
		return nil
	}
	switch m := v.(type) {
	case map[string]string:
		out := make(map[string]string, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, val := range m {
			s, ok := val.(string)
			if ok {
				out[k] = s
			}
		}
		return out
	default:
		return nil
	}
}

func expandHomeSlice(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = config.ExpandHomePath(value)
	}
	return out
}

func expandHomeMapValues(values map[string]string) map[string]string {
	if len(values) == 0 {
		return values
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = config.ExpandHomePath(value)
	}
	return out
}

func intField(cfg map[string]any, key string) int {
	v, ok := cfg[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func boolField(cfg map[string]any, key string) bool {
	v, ok := cfg[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// boolFieldDefault is like boolField but returns the caller-supplied default
// when the key is absent. Use this when the documented default for the field
// is true (or otherwise non-zero), so absent-in-YAML doesn't collapse to false.
func boolFieldDefault(cfg map[string]any, key string, defaultValue bool) bool {
	v, ok := cfg[key]
	if !ok {
		return defaultValue
	}
	b, ok := v.(bool)
	if !ok {
		return defaultValue
	}
	return b
}

func healthCheckFromAny(v any) (config.HealthCheck, error) {
	switch raw := v.(type) {
	case map[string]any:
		hc := config.HealthCheck{}
		if s, ok := raw["url"].(string); ok {
			hc.URL = s
		}
		switch cmd := raw["command"].(type) {
		case []string:
			hc.Command = append([]string(nil), cmd...)
		case []any:
			for _, item := range cmd {
				s, ok := item.(string)
				if ok {
					hc.Command = append(hc.Command, s)
				}
			}
		}
		if s, ok := raw["interval"].(string); ok {
			hc.Interval = s
		}
		if s, ok := raw["timeout"].(string); ok {
			hc.Timeout = s
		}
		return hc, nil
	default:
		return config.HealthCheck{}, fmt.Errorf("unsupported type %T", v)
	}
}
