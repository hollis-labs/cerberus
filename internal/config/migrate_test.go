package config

import (
	"testing"
)

func TestMigrateProjectDeduplication(t *testing.T) {
	v1 := &Config{
		Version: 1,
		Services: []ServiceDef{
			{ID: "svc1", Name: "Svc 1", Project: "alpha", Dir: "/tmp/a", Command: []string{"echo"}},
			{ID: "svc2", Name: "Svc 2", Project: "alpha", Dir: "/tmp/b", Command: []string{"echo"}},
			{ID: "svc3", Name: "Svc 3", Project: "beta", Dir: "/tmp/c", Command: []string{"echo"}},
		},
	}

	v2 := MigrateV1ToV2(v1)

	if v2.Version != 2 {
		t.Errorf("expected version 2, got %d", v2.Version)
	}
	if len(v2.Projects) != 2 {
		t.Fatalf("expected 2 deduplicated projects, got %d", len(v2.Projects))
	}
	if v2.Projects[0].ID != "alpha" || v2.Projects[1].ID != "beta" {
		t.Errorf("unexpected project IDs: %v, %v", v2.Projects[0].ID, v2.Projects[1].ID)
	}
}

func TestMigrateAllFieldsPopulated(t *testing.T) {
	v1 := &Config{
		Version: 1,
		Services: []ServiceDef{
			{
				ID:      "full",
				Name:    "Full Service",
				Project: "proj",
				Dir:     "/tmp/full",
				Command: []string{"go", "run", "."},
				EnvFile: ".env",
				Env:     map[string]string{"FOO": "bar"},
				URL:     "http://localhost:8080",
				Port:    8080,
				Tags:    []string{"backend", "api"},
				Build:   []string{"go", "build", "."},
				Health:  "http://localhost:8080/health",
				HealthCheckCfg: HealthCheck{
					URL:      "http://localhost:8080/healthz",
					Command:  []string{"curl", "-f", "http://localhost:8080/healthz"},
					Interval: "10s",
					Timeout:  "3s",
				},
				DependsOn:          []string{"db", "cache"},
				AutoStart:          true,
				AutoRestart:        true,
				RestartDelay:       "5s",
				MaxRestartAttempts: 3,
				RestartCooldown:    "30s",
				LogFile:            "/var/log/full.log",
				Profiles:           []string{"default", "backend"},
				Protected:          true,
			},
		},
	}

	v2 := MigrateV1ToV2(v1)

	if len(v2.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(v2.Resources))
	}

	r := v2.Resources[0]
	if r.ID != "full" {
		t.Errorf("expected id full, got %s", r.ID)
	}
	if r.Type != "process" {
		t.Errorf("expected type process, got %s", r.Type)
	}
	if r.Connector != "local" {
		t.Errorf("expected connector local, got %s", r.Connector)
	}
	if r.Project != "proj" {
		t.Errorf("expected project proj, got %s", r.Project)
	}
	if len(r.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(r.Tags))
	}
	if len(r.DependsOn) != 2 {
		t.Errorf("expected 2 depends_on, got %d", len(r.DependsOn))
	}

	// Check all config map fields.
	cfg := r.Config
	checks := map[string]bool{
		"dir":                  true,
		"command":              true,
		"port":                 true,
		"url":                  true,
		"env_file":             true,
		"env":                  true,
		"build":                true,
		"health":               true,
		"health_check":         true,
		"auto_start":           true,
		"auto_restart":         true,
		"restart_delay":        true,
		"max_restart_attempts": true,
		"restart_cooldown":     true,
		"log_file":             true,
		"profiles":             true,
		"protected":            true,
	}
	for key := range checks {
		if _, ok := cfg[key]; !ok {
			t.Errorf("expected config key %q to be present", key)
		}
	}
}

func TestMigrateEmptyFieldsOmitted(t *testing.T) {
	v1 := &Config{
		Version: 1,
		Services: []ServiceDef{
			{
				ID:      "minimal",
				Name:    "Minimal",
				Project: "proj",
				Dir:     "/tmp/min",
				Command: []string{"echo"},
			},
		},
	}

	v2 := MigrateV1ToV2(v1)
	r := v2.Resources[0]
	cfg := r.Config

	// These should be present (non-zero).
	if _, ok := cfg["dir"]; !ok {
		t.Error("expected dir in config")
	}
	if _, ok := cfg["command"]; !ok {
		t.Error("expected command in config")
	}

	// These should be absent (zero values).
	absent := []string{
		"port", "url", "env_file", "env", "build", "health",
		"health_check", "auto_start", "auto_restart", "restart_delay",
		"max_restart_attempts", "restart_cooldown", "log_file",
		"profiles", "protected",
	}
	for _, key := range absent {
		if _, ok := cfg[key]; ok {
			t.Errorf("expected config key %q to be absent for zero value", key)
		}
	}
}

func TestMigrateNoVersionTreatedAsV1(t *testing.T) {
	v1 := &Config{
		Version: 0, // no version field
		Services: []ServiceDef{
			{ID: "svc", Name: "Svc", Project: "proj", Dir: "/tmp", Command: []string{"echo"}},
		},
	}

	v2 := MigrateV1ToV2(v1)
	if v2.Version != 2 {
		t.Errorf("expected version 2, got %d", v2.Version)
	}
	if len(v2.Resources) != 1 {
		t.Errorf("expected 1 resource, got %d", len(v2.Resources))
	}
}

func TestMigrateResourceCountMatchesServiceCount(t *testing.T) {
	v1 := &Config{
		Version: 1,
		Services: []ServiceDef{
			{ID: "a", Name: "A", Project: "p1", Dir: "/a", Command: []string{"a"}},
			{ID: "b", Name: "B", Project: "p1", Dir: "/b", Command: []string{"b"}},
			{ID: "c", Name: "C", Project: "p2", Dir: "/c", Command: []string{"c"}},
			{ID: "d", Name: "D", Project: "p2", Dir: "/d", Command: []string{"d"}},
			{ID: "e", Name: "E", Project: "p3", Dir: "/e", Command: []string{"e"}},
		},
	}

	v2 := MigrateV1ToV2(v1)
	if len(v2.Resources) != len(v1.Services) {
		t.Errorf("resource count %d != service count %d", len(v2.Resources), len(v1.Services))
	}
}

func TestMigratePortZeroOmitted(t *testing.T) {
	// CRITICAL: port 0 must never appear in config map.
	// See CLAUDE.md for why — lsof -ti :0 returns random system PIDs.
	v1 := &Config{
		Version: 1,
		Services: []ServiceDef{
			{ID: "daemon", Name: "Daemon", Project: "proj", Dir: "/tmp", Command: []string{"d"}, Port: 0},
		},
	}

	v2 := MigrateV1ToV2(v1)
	cfg := v2.Resources[0].Config
	if _, ok := cfg["port"]; ok {
		t.Error("port 0 must be omitted from config map — lsof -ti :0 causes false positives")
	}
}

func TestLoadUnifiedV1(t *testing.T) {
	yamlContent := `
version: 1
services:
  - id: foo
    name: Foo
    project: proj
    dir: /tmp/foo
    command: ["echo", "hi"]
    port: 9000
`
	v2, err := LoadUnified(writeTempConfig(t, yamlContent))
	if err != nil {
		t.Fatalf("LoadUnified failed: %v", err)
	}
	if v2.Version != 2 {
		t.Errorf("expected version 2, got %d", v2.Version)
	}
	if len(v2.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(v2.Resources))
	}
	if v2.Resources[0].ID != "foo" {
		t.Errorf("expected resource id foo, got %s", v2.Resources[0].ID)
	}
}

func TestLoadUnifiedV2(t *testing.T) {
	yamlContent := `
version: 2
projects:
  - id: proj
    name: Project
resources:
  - id: foo
    name: Foo
    type: process
    project: proj
    connector: local
    config:
      dir: /tmp/foo
      command: ["echo", "hi"]
`
	v2, err := LoadUnified(writeTempConfig(t, yamlContent))
	if err != nil {
		t.Fatalf("LoadUnified failed: %v", err)
	}
	if v2.Version != 2 {
		t.Errorf("expected version 2, got %d", v2.Version)
	}
	if len(v2.Projects) != 1 {
		t.Errorf("expected 1 project, got %d", len(v2.Projects))
	}
	if len(v2.Resources) != 1 {
		t.Errorf("expected 1 resource, got %d", len(v2.Resources))
	}
}

func TestLoadUnifiedNoVersion(t *testing.T) {
	yamlContent := `
services:
  - id: svc
    name: Svc
    project: proj
    dir: /tmp
    command: ["echo"]
`
	v2, err := LoadUnified(writeTempConfig(t, yamlContent))
	if err != nil {
		t.Fatalf("LoadUnified failed: %v", err)
	}
	if v2.Version != 2 {
		t.Errorf("expected version 2 after migration, got %d", v2.Version)
	}
}

func TestLoadUnifiedUnsupportedVersion(t *testing.T) {
	yamlContent := `
version: 99
services: []
`
	_, err := LoadUnified(writeTempConfig(t, yamlContent))
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
}
