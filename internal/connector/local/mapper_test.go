package local

import (
	"testing"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/domain"
)

func TestServiceDefToResource(t *testing.T) {
	def := config.ServiceDef{
		ID:      "engine",
		Name:    "Engine",
		Project: "tiamat",
		Dir:     "/home/user/engine",
		Command: []string{"engine", "serve"},
		Port:    8080,
		Tags:    []string{"backend", "go"},
		Build:   []string{"go", "install", "./cmd/engine"},
		HealthCheckCfg: config.HealthCheck{
			URL:      "http://127.0.0.1:8080/health",
			Interval: "10s",
		},
		DependsOn:   []string{"db"},
		AutoRestart: true,
		Protected:   true,
	}

	res := ServiceDefToResource(def)

	if res.ID != "engine" {
		t.Errorf("ID = %q, want %q", res.ID, "engine")
	}
	if res.Type != domain.ResourceProcess {
		t.Errorf("Type = %q, want %q", res.Type, domain.ResourceProcess)
	}
	if res.Connector != "local" {
		t.Errorf("Connector = %q, want %q", res.Connector, "local")
	}
	if res.ProjectID != "tiamat" {
		t.Errorf("ProjectID = %q, want %q", res.ProjectID, "tiamat")
	}
	if res.Config["port"] != 8080 {
		t.Errorf("Config[port] = %v, want 8080", res.Config["port"])
	}
	if res.Config["auto_restart"] != true {
		t.Error("Config[auto_restart] should be true")
	}
	if res.Config["protected"] != true {
		t.Error("Config[protected] should be true")
	}

	hc, ok := res.Config["health_check"].(map[string]any)
	if !ok {
		t.Fatal("Config[health_check] should be a map")
	}
	if hc["url"] != "http://127.0.0.1:8080/health" {
		t.Errorf("health_check.url = %v", hc["url"])
	}
}

func TestServiceDefToResourceOmitsPortZero(t *testing.T) {
	def := config.ServiceDef{
		ID:      "daemon",
		Name:    "Daemon",
		Project: "test",
		Dir:     "/tmp",
		Command: []string{"./daemon"},
		Port:    0,
	}

	res := ServiceDefToResource(def)

	if _, exists := res.Config["port"]; exists {
		t.Error("Config should NOT contain port when port is 0")
	}
}

func TestServiceDefToResourceOmitsEmptyFields(t *testing.T) {
	def := config.ServiceDef{
		ID:      "minimal",
		Name:    "Minimal",
		Project: "test",
		Dir:     "/tmp",
		Command: []string{"./run"},
	}

	res := ServiceDefToResource(def)

	for _, key := range []string{"env_file", "env", "url", "port", "build", "health", "health_check",
		"auto_start", "auto_restart", "restart_delay", "max_restart_attempts",
		"restart_cooldown", "log_file", "profiles", "protected"} {
		if _, exists := res.Config[key]; exists {
			t.Errorf("Config should NOT contain %q for minimal service", key)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	original := config.ServiceDef{
		ID:      "conduit",
		Name:    "Vanta Conduit",
		Project: "tiamat",
		Dir:     "/home/user/conduit",
		Command: []string{"conduit", "serve"},
		EnvFile: ".env",
		Env:     map[string]string{"GO_ENV": "development"},
		URL:     "http://localhost:8082",
		Port:    8082,
		Tags:    []string{"backend"},
		Build:   []string{"go", "install", "./cmd/conduit"},
		HealthCheckCfg: config.HealthCheck{
			URL:      "http://127.0.0.1:8082/health",
			Interval: "10s",
			Timeout:  "5s",
		},
		DependsOn:          []string{"engine"},
		AutoStart:          true,
		AutoRestart:        true,
		RestartDelay:       "5s",
		MaxRestartAttempts: 3,
		RestartCooldown:    "60s",
		LogFile:            "/tmp/conduit.log",
		Profiles:           []string{"dev"},
		Protected:          true,
	}

	res := ServiceDefToResource(original)
	restored := ResourceToServiceDef(res)

	if restored.ID != original.ID {
		t.Errorf("ID: got %q, want %q", restored.ID, original.ID)
	}
	if restored.Dir != original.Dir {
		t.Errorf("Dir: got %q, want %q", restored.Dir, original.Dir)
	}
	if restored.Port != original.Port {
		t.Errorf("Port: got %d, want %d", restored.Port, original.Port)
	}
	if restored.EnvFile != original.EnvFile {
		t.Errorf("EnvFile: got %q, want %q", restored.EnvFile, original.EnvFile)
	}
	if restored.AutoRestart != original.AutoRestart {
		t.Errorf("AutoRestart: got %v, want %v", restored.AutoRestart, original.AutoRestart)
	}
	if restored.Protected != original.Protected {
		t.Errorf("Protected: got %v, want %v", restored.Protected, original.Protected)
	}
	if restored.MaxRestartAttempts != original.MaxRestartAttempts {
		t.Errorf("MaxRestartAttempts: got %d, want %d", restored.MaxRestartAttempts, original.MaxRestartAttempts)
	}
	if restored.HealthCheckCfg.URL != original.HealthCheckCfg.URL {
		t.Errorf("HealthCheck.URL: got %q, want %q", restored.HealthCheckCfg.URL, original.HealthCheckCfg.URL)
	}
	if len(restored.Command) != len(original.Command) {
		t.Errorf("Command length: got %d, want %d", len(restored.Command), len(original.Command))
	}
	if len(restored.DependsOn) != len(original.DependsOn) {
		t.Errorf("DependsOn length: got %d, want %d", len(restored.DependsOn), len(original.DependsOn))
	}
}
