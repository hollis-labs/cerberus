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
	Port    int               `yaml:"port"`
	Tags    []string          `yaml:"tags,omitempty"`
	Build   []string          `yaml:"build,omitempty"`

	// Legacy field — still parsed for backward compat.
	// If health_check is empty, Health is mapped to HealthCheck.URL.
	Health string `yaml:"health,omitempty"`

	// New v1 fields
	HealthCheckCfg HealthCheck `yaml:"health_check,omitempty"`
	DependsOn      []string    `yaml:"depends_on,omitempty"`
	AutoStart      bool        `yaml:"auto_start,omitempty"`
	AutoRestart    bool        `yaml:"auto_restart,omitempty"`
	RestartDelay   string      `yaml:"restart_delay,omitempty"`
	Profiles       []string    `yaml:"profiles,omitempty"`
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
#   8095  | Hadron Daemon
#   9085  | Volon gRPC (started by volon-api)
#   34116 | Hadron Frontend (Wails/Vite)
#   5174  | Carrier Frontend (Vite)
#   5176  | Mentat Chat Frontend (Vite)
#   8090  | Mentat Chat API (Go)
#   8096  | Carrier API (Python)

services:
  # --- Volon (orchestration) ---
  - id: volon-api
    name: "Volon API"
    project: volon
    dir: ~/Projects-apps/volon
    command: ["./gui-server", "--repo", ".", "--http", ":8085"]
    build: ["go", "build", "-o", "gui-server", "./cmd/gui-server"]
    env_file: .env.local
    url: http://127.0.0.1:8085
    port: 8085
    health: http://127.0.0.1:8085/v1/tasks
    tags: [api, daemon, go]
    # depends_on: [cortex-api]
    # auto_start: true
    # auto_restart: true
    # restart_delay: "5s"
    # health_check:
    #   url: http://127.0.0.1:8085/v1/tasks
    #   interval: "10s"
    #   timeout: "3s"
    # profiles: [default, backend]

  - id: volon-frontend
    name: "Volon Frontend"
    project: volon
    dir: ~/Projects-apps/volon/apps/gui
    command: ["npm", "run", "dev"]
    url: http://127.0.0.1:1420
    port: 1420
    tags: [gui, frontend, vite]

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
    build: ["go", "build", "-o", "contextd", "./cmd/contextd/"]
    command: ["./contextd", "serve", "--addr", ":8080"]
    env:
      CONTEXTD_ROOT: ~/.cortex
    url: http://127.0.0.1:8080
    port: 8080
    health: http://127.0.0.1:8080/v1/health/readiness
    tags: [api, daemon, go]

  - id: cortex-frontend
    name: "Cortex Frontend"
    project: cortex
    dir: ~/Projects-apps/cortex/frontend
    command: ["npm", "run", "dev"]
    url: http://localhost:5173
    port: 5173
    tags: [gui, frontend, vite]

  # --- Carrier (content-ops) ---
  - id: carrier-api
    name: "Carrier API"
    project: carrier
    dir: ~/Projects-apps/carrier
    command: ["./bin/carrier", "serve", "--config", "config/config.yaml", "--repos", "config/repos.yaml"]
    url: http://127.0.0.1:8096
    port: 8096
    tags: [api, python]

  - id: carrier-frontend
    name: "Carrier Frontend"
    project: carrier
    dir: ~/Projects-apps/carrier/frontend
    command: ["npm", "run", "dev"]
    url: http://localhost:5174
    port: 5174
    tags: [gui, frontend, vite]

  # --- Nanite (notes) ---
  - id: nanite-dev
    name: "Nanite Dev"
    project: nanite
    dir: ~/Projects-apps/nanite
    command: ["wails", "dev"]
    port: 7765
    tags: [gui, desktop, wails]

  # --- Mentat Chat ---
  - id: mentat-api
    name: "Mentat Chat API"
    project: mentat-chat
    dir: ~/Projects-apps/mentat-chat
    command: ["go", "run", "./cmd/mentat-chat", "serve"]
    build: ["go", "build", "-o", "bin/mentat-chat", "./cmd/mentat-chat"]
    url: http://127.0.0.1:8090
    port: 8090
    health: http://127.0.0.1:8090/api/health
    tags: [api, go]

  - id: mentat-frontend
    name: "Mentat Chat Frontend"
    project: mentat-chat
    dir: ~/Projects-apps/mentat-chat/ui
    command: ["npm", "run", "dev"]
    url: http://localhost:5176
    port: 5176
    tags: [gui, frontend, vite]
`
