package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// HealthCheck defines how Cerberus checks whether a service is alive.
type HealthCheck struct {
	URL      string   `yaml:"url,omitempty"`
	Command  []string `yaml:"command,omitempty"`
	Interval string   `yaml:"interval,omitempty"`
	Timeout  string   `yaml:"timeout,omitempty"`
}

type ServiceDef struct {
	ID      string            `yaml:"id"`
	Name    string            `yaml:"name"`
	Project string            `yaml:"project"`
	Dir     string            `yaml:"dir"`
	Command []string          `yaml:"command"`
	EnvFile string            `yaml:"env_file,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	URL     string            `yaml:"url,omitempty"`
	// Port is the TCP port the service listens on. Used for status detection via lsof.
	// IMPORTANT: Omit this field for services that don't listen on a port.
	// Do NOT set port: 0 — `lsof -ti :0` returns random system PIDs, causing
	// false-positive "running" status in the TUI and daemon monitor.
	Port  int      `yaml:"port,omitempty"`
	Tags  []string `yaml:"tags,omitempty"`
	Build []string `yaml:"build,omitempty"`

	// Legacy field — still parsed for backward compat.
	// If health_check is empty, Health is mapped to HealthCheck.URL.
	Health string `yaml:"health,omitempty"`

	// New v1 fields
	HealthCheckCfg     HealthCheck `yaml:"health_check,omitempty"`
	DependsOn          []string    `yaml:"depends_on,omitempty"`
	AutoStart          bool        `yaml:"auto_start,omitempty"`
	AutoRestart        bool        `yaml:"auto_restart,omitempty"`
	RestartDelay       string      `yaml:"restart_delay,omitempty"`
	MaxRestartAttempts int         `yaml:"max_restart_attempts,omitempty"`
	RestartCooldown    string      `yaml:"restart_cooldown,omitempty"`
	LogFile            string      `yaml:"log_file,omitempty"`
	Profiles           []string    `yaml:"profiles,omitempty"`
	Protected          bool        `yaml:"protected,omitempty"`
}

type Config struct {
	Version  int          `yaml:"version,omitempty"`
	Services []ServiceDef `yaml:"services"`
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cerberus", "config.yaml")
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Default version to 1 if omitted
	if cfg.Version == 0 {
		cfg.Version = 1
	}

	for i := range cfg.Services {
		svc := &cfg.Services[i]

		svc.Dir = ExpandHomePath(svc.Dir)
		svc.LogFile = ExpandHomePath(svc.LogFile)
		svc.Env = expandHomeMapValues(svc.Env)
		svc.Command = expandHomeSlice(svc.Command)
		svc.Build = expandHomeSlice(svc.Build)
		svc.EnvFile = ExpandHomePath(svc.EnvFile)
		svc.HealthCheckCfg.Command = expandHomeSlice(svc.HealthCheckCfg.Command)

		// Backward compat: map legacy Health field → HealthCheck.URL
		if svc.Health != "" && svc.HealthCheckCfg.URL == "" && len(svc.HealthCheckCfg.Command) == 0 {
			svc.HealthCheckCfg.URL = svc.Health
		}
	}

	return &cfg, nil
}

// versionProbe is used to peek at the version field before full parsing.
type versionProbe struct {
	Version int `yaml:"version"`
}

// LoadUnified loads a v2 config file.
func LoadUnified(path string) (*ConfigV2, error) {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var probe versionProbe
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parse config version: %w", err)
	}

	switch probe.Version {
	case 2:
		var v2 ConfigV2
		if err := yaml.Unmarshal(data, &v2); err != nil {
			return nil, fmt.Errorf("parse v2 config: %w", err)
		}
		normalizeV2Config(&v2)
		return &v2, nil

	default:
		return nil, fmt.Errorf("unsupported config version: %d (Cerberus now requires version: 2)", probe.Version)
	}
}

func EnsureDefault() error {
	path := DefaultPath()
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, []byte(defaultConfig), 0644)
}

const defaultConfig = `# Cerberus — Fragments Engine Local Runtime
# This is a SEED template. It is only written when ~/.cerberus/config.yaml
# does not exist. The live config is ALWAYS ~/.cerberus/config.yaml.
# Edit that file directly — changes here have NO effect on running systems.
version: 2

projects: []
resources: []
`
