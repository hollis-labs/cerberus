package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

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
	Health  string            `yaml:"health,omitempty"`
	Build   []string          `yaml:"build,omitempty"`
}

type Config struct {
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

	// Expand ~ in dir paths
	home, _ := os.UserHomeDir()
	for i := range cfg.Services {
		if strings.HasPrefix(cfg.Services[i].Dir, "~/") {
			cfg.Services[i].Dir = filepath.Join(home, cfg.Services[i].Dir[2:])
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

const defaultConfig = `# Cerberus - suite Service Manager
# Port map for Project suite (all unique, no collisions)
#
#   Port  | Service
#   ------|------------------
#   1420  | app-h Frontend (Vite)
#   5173  | Cortex Frontend (Vite)
#   7765  | app-a API (embedded)
#   8080  | Cortex API
#   8085  | app-h API (Go backend)
#   8095  | app-d Daemon
#   9085  | app-h gRPC (started by app-h-api)
#   34116 | app-d Frontend (Wails/Vite)
#   5174  | Carrier Frontend (Vite)
#   8096  | Carrier API (Python)

services:
  # --- app-h (orchestration) ---
  - id: app-h-api
    name: "app-h API"
    project: app-h
    dir: ~/src/app-h
    command: ["go", "run", "./cmd/gui-server", "--repo", ".", "--http", ":8085"]
    build: ["go", "build", "-o", "gui-server", "./cmd/gui-server"]
    env_file: .env.local
    url: http://127.0.0.1:8085
    port: 8085
    health: http://127.0.0.1:8085/v1/tasks
    tags: [api, daemon, go]

  - id: app-h-frontend
    name: "app-h Frontend"
    project: app-h
    dir: ~/src/app-h/apps/gui
    command: ["npm", "run", "dev"]
    url: http://127.0.0.1:1420
    port: 1420
    tags: [gui, frontend, vite]

  # --- app-d (automation) ---
  - id: app-d-daemon
    name: "app-d Daemon"
    project: app-d
    dir: ~/src/app-d
    command: ["./bin/hadrond", "serve"]
    build: ["go", "build", "-o", "bin/hadrond", "./cmd/hadrond"]
    url: http://127.0.0.1:8095
    port: 8095
    health: http://127.0.0.1:8095/
    tags: [daemon, api, go]

  - id: app-d-gui
    name: "app-d GUI"
    project: app-d
    dir: ~/src/app-d/cmd/app-d-app
    command: ["wails", "dev"]
    port: 34116
    tags: [gui, desktop, wails]

  # --- Cortex (memory) ---
  - id: cortex-api
    name: "Cortex API"
    project: cortex
    dir: ~/src/cortex
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
    dir: ~/src/cortex/frontend
    command: ["npm", "run", "dev"]
    url: http://localhost:5173
    port: 5173
    tags: [gui, frontend, vite]

  # --- Carrier (content-ops) ---
  - id: carrier-api
    name: "Carrier API"
    project: carrier
    dir: ~/src/content-ops
    command: ["./bin/carrier", "serve", "--config", "config/config.yaml", "--repos", "config/repos.yaml"]
    url: http://127.0.0.1:8096
    port: 8096
    tags: [api, python]

  - id: carrier-frontend
    name: "Carrier Frontend"
    project: carrier
    dir: ~/src/content-ops/tasks/contentops-frontend-template
    command: ["npm", "run", "dev"]
    url: http://localhost:5174
    port: 5174
    tags: [gui, frontend, vite]

  # --- app-a (notes) ---
  - id: app-a-dev
    name: "app-a Dev"
    project: app-a
    dir: ~/src/app-a
    command: ["wails", "dev"]
    port: 7765
    tags: [gui, desktop, wails]
`
