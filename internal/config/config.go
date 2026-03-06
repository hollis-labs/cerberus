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

const defaultConfig = `# Cerberus - Tiamat Service Manager
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
`
