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
	Build              []string
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
	spec.Build = stringSliceField(cfg, "build")
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
	if len(s.Build) > 0 {
		cfg["build"] = append([]string(nil), s.Build...)
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
