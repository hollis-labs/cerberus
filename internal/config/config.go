package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	Port int `yaml:"port,omitempty"`
	Tags    []string          `yaml:"tags,omitempty"`
	Build   []string          `yaml:"build,omitempty"`

	// Legacy field — still parsed for backward compat.
	// If health_check is empty, Health is mapped to HealthCheck.URL.
	Health string `yaml:"health,omitempty"`

	// New v1 fields
	HealthCheckCfg    HealthCheck `yaml:"health_check,omitempty"`
	DependsOn         []string    `yaml:"depends_on,omitempty"`
	AutoStart         bool        `yaml:"auto_start,omitempty"`
	AutoRestart       bool        `yaml:"auto_restart,omitempty"`
	RestartDelay      string      `yaml:"restart_delay,omitempty"`
	MaxRestartAttempts int        `yaml:"max_restart_attempts,omitempty"`
	RestartCooldown   string      `yaml:"restart_cooldown,omitempty"`
	LogFile           string      `yaml:"log_file,omitempty"`
	Profiles          []string    `yaml:"profiles,omitempty"`
	Protected         bool        `yaml:"protected,omitempty"`
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

	home, _ := os.UserHomeDir()
	for i := range cfg.Services {
		svc := &cfg.Services[i]

		// Expand ~ in dir paths
		if strings.HasPrefix(svc.Dir, "~/") {
			svc.Dir = filepath.Join(home, svc.Dir[2:])
		}

		// Expand ~ in log_file paths
		if strings.HasPrefix(svc.LogFile, "~/") {
			svc.LogFile = filepath.Join(home, svc.LogFile[2:])
		}

		// Backward compat: map legacy Health field → HealthCheck.URL
		if svc.Health != "" && svc.HealthCheckCfg.URL == "" && len(svc.HealthCheckCfg.Command) == 0 {
			svc.HealthCheckCfg.URL = svc.Health
		}
	}

	return &cfg, nil
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

const defaultConfig = `# Cerberus - Tiamat Service Manager
# Config version (currently 1)
version: 1

# Port map for Project Tiamat (all unique, no collisions)
#
#   Port  | Service
#   ------|------------------
#   1420  | Volon Frontend (Vite)
#   5173  | Cortex Frontend (Vite)
#   7765  | Nanite API (embedded)
#   8080  | Cortex API
#   8085  | Volon API (Go backend)
#   8086  | Volon Scheduler (standalone daemon)
#   8090  | Mentat API (Go)
#   8095  | Hadron Daemon
#   8096  | Carrier API (Python)
#   9085  | Volon gRPC (started by volon-api)
#   34116 | Hadron Frontend (Wails/Vite)
#   5174  | Carrier Frontend (Vite)
#   5176  | Mentat Frontend (Vite)
#
# IMPORTANT: Do NOT set "port: 0" on any service. Omit the port field entirely
# for services that don't listen on a port (e.g. CLI tools, daemons without
# an HTTP interface). lsof -ti :0 returns arbitrary system PIDs, which causes
# false-positive "running" status in the TUI and daemon monitor.

services:
  # --- Volon (orchestration) ---
  - id: volon-api
    name: "Volon API"
    project: volon
    dir: ~/Projects-apps/volon
    command: ["./scripts/volon-api-wrapper.sh", "--repo", ".", "--http", ":8085"]
    build: ["sh", "-c", "go build -o gui-server ./cmd/gui-server && go build -o volon ./cmd/volon"]
    env_file: .env.local
    url: http://127.0.0.1:8085
    port: 8085
    health: http://127.0.0.1:8085/v1/tasks
    tags: [api, daemon, go, volon-go]
    protected: true
    auto_restart: true

  - id: volon-scheduler
    name: "Volon Scheduler"
    project: volon
    dir: ~/Projects-apps/volon
    command: ["./scripts/volon-scheduler-wrapper.sh", "--repo", ".", "--health-addr", ":8086"]
    build: ["sh", "-c", "go build -o volon-scheduler ./cmd/scheduler && go build -o volon ./cmd/volon"]
    env_file: .env.local
    env:
      OTEL_SDK_DISABLED: "true"
    url: http://127.0.0.1:8086
    port: 8086
    health: http://127.0.0.1:8086/v1/health
    log_file: ~/Projects-apps/volon/logs/scheduler.log
    tags: [daemon, go, scheduler, volon-go]
    depends_on: [volon-api]
    auto_start: false
    auto_restart: true
    restart_delay: "5s"
    protected: true

  - id: volon-frontend
    name: "Volon Frontend"
    project: volon
    dir: ~/Projects-apps/volon/apps/gui
    command: ["npm", "run", "dev"]
    url: http://127.0.0.1:1420
    port: 1420
    tags: [gui, frontend, vite]
    auto_restart: true

  # --- Hadron (automation) ---
  - id: hadron-daemon
    name: "Hadron Daemon"
    project: hadron
    dir: ~/Projects-apps/hadron
    command: ["./bin/hadrond", "serve"]
    build: ["go", "build", "-o", "bin/hadrond", "./cmd/hadrond"]
    url: http://127.0.0.1:8095
    port: 8095
    health: http://127.0.0.1:8095/
    tags: [daemon, api, go]
    protected: true
    auto_restart: true

  - id: hadron-gui
    name: "Hadron GUI"
    project: hadron
    dir: ~/Projects-apps/hadron/cmd/hadron-app
    command: ["wails", "dev"]
    env:
      HADRON_DAEMON_EXTERNAL: "true"
    port: 34116
    tags: [gui, desktop, wails]

  # --- Cortex (memory) ---
  - id: cortex-api
    name: "Cortex API"
    project: cortex
    dir: ~/Projects-apps/cortex
    command: ["./contextd", "serve", "--addr", ":8080"]
    build: ["go", "build", "-o", "contextd", "./cmd/contextd/"]
    env:
      CONTEXTD_ROOT: ~/.cortex
    url: http://127.0.0.1:8080
    port: 8080
    health: http://127.0.0.1:8080/v1/health/readiness
    tags: [api, daemon, go]
    protected: true
    auto_restart: true

  - id: cortex-frontend
    name: "Cortex Frontend"
    project: cortex
    dir: ~/Projects-apps/cortex/frontend
    command: ["npm", "run", "dev"]
    url: http://localhost:5173
    port: 5173
    tags: [gui, frontend, vite]
    auto_restart: true

  # --- Carrier (content-ops) ---
  - id: carrier-api
    name: "Carrier API"
    project: carrier
    dir: ~/Projects-apps/carrier
    command: ["./bin/carrier", "serve", "--config", "config/config.yaml", "--repos", "config/repos.yaml"]
    url: http://127.0.0.1:8096
    port: 8096
    tags: [api, python]
    auto_restart: true

  - id: carrier-frontend
    name: "Carrier Frontend"
    project: carrier
    dir: ~/Projects-apps/carrier/frontend
    command: ["npm", "run", "dev"]
    url: http://localhost:5174
    port: 5174
    tags: [gui, frontend, vite]
    auto_restart: true

  # --- Mentat (meta-agent) ---
  - id: mentat-api
    name: "Mentat API"
    project: mentat
    dir: ~/Projects-apps/mentat
    command: ["./mentat", "serve"]
    build: ["go", "build", "-o", "mentat", "./cmd/mentat"]
    url: http://127.0.0.1:8090
    port: 8090
    health: http://127.0.0.1:8090/api/health
    tags: [api, daemon, go]
    auto_restart: true

  - id: mentat-frontend
    name: "Mentat Frontend"
    project: mentat
    dir: ~/Projects-apps/mentat/ui
    command: ["npm", "run", "dev"]
    url: http://localhost:5176
    port: 5176
    tags: [gui, frontend, vite]
    auto_restart: true

  # --- Cerberus (self-managed daemon) ---
  # No port field — cerberus daemon doesn't expose an HTTP port.
  # Status detection uses PID file only.
  - id: cerberus-daemon
    name: "Cerberus Daemon"
    project: cerberus
    dir: ~/Projects-apps/cerberus
    command: ["./cerberus", "daemon"]
    build: ["go", "build", "-o", "cerberus", "./cmd/cerberus"]
    tags: [daemon, go, infrastructure]
    protected: true
    auto_restart: true

  # --- Nanite (notes) ---
  - id: nanite-dev
    name: "Nanite Dev"
    project: nanite
    dir: ~/Projects-apps/nanite
    command: ["wails", "dev"]
    port: 7765
    tags: [gui, desktop, wails]
`
