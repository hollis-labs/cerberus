package config

import (
	"os"
	"path/filepath"
	"testing"
)

// helper: write yaml to a temp file and return the path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadOldFormatNoVersion(t *testing.T) {
	yaml := `
services:
  - id: foo
    name: Foo
    project: proj
    dir: /tmp/foo
    command: ["echo", "hi"]
    port: 9000
    health: http://localhost:9000/health
`
	cfg, err := Load(writeTempConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Version should default to 1
	if cfg.Version != 1 {
		t.Errorf("expected version 1, got %d", cfg.Version)
	}

	if len(cfg.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(cfg.Services))
	}

	svc := cfg.Services[0]
	if svc.ID != "foo" {
		t.Errorf("expected id foo, got %s", svc.ID)
	}
	if svc.Health != "http://localhost:9000/health" {
		t.Errorf("expected legacy health field preserved, got %s", svc.Health)
	}
}

func TestLoadNewFormatAllFields(t *testing.T) {
	yaml := `
version: 1
services:
  - id: bar
    name: Bar
    project: proj
    dir: /tmp/bar
    command: ["go", "run", "."]
    port: 8080
    depends_on: [baz, qux]
    auto_start: true
    auto_restart: true
    restart_delay: "5s"
    profiles: [default, backend]
    health_check:
      url: http://localhost:8080/health
      command: ["curl", "-f", "http://localhost:8080/health"]
      interval: "10s"
      timeout: "3s"
`
	cfg, err := Load(writeTempConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Version != 1 {
		t.Errorf("expected version 1, got %d", cfg.Version)
	}

	svc := cfg.Services[0]

	// depends_on
	if len(svc.DependsOn) != 2 || svc.DependsOn[0] != "baz" || svc.DependsOn[1] != "qux" {
		t.Errorf("depends_on mismatch: %v", svc.DependsOn)
	}

	// auto flags
	if !svc.AutoStart {
		t.Error("expected auto_start true")
	}
	if !svc.AutoRestart {
		t.Error("expected auto_restart true")
	}
	if svc.RestartDelay != "5s" {
		t.Errorf("expected restart_delay 5s, got %s", svc.RestartDelay)
	}

	// profiles
	if len(svc.Profiles) != 2 || svc.Profiles[0] != "default" || svc.Profiles[1] != "backend" {
		t.Errorf("profiles mismatch: %v", svc.Profiles)
	}

	// health_check
	hc := svc.HealthCheckCfg
	if hc.URL != "http://localhost:8080/health" {
		t.Errorf("health_check.url mismatch: %s", hc.URL)
	}
	if len(hc.Command) != 3 {
		t.Errorf("health_check.command mismatch: %v", hc.Command)
	}
	if hc.Interval != "10s" {
		t.Errorf("health_check.interval mismatch: %s", hc.Interval)
	}
	if hc.Timeout != "3s" {
		t.Errorf("health_check.timeout mismatch: %s", hc.Timeout)
	}
}

func TestHealthFieldBackwardCompat(t *testing.T) {
	yaml := `
services:
  - id: legacy
    name: Legacy
    project: proj
    dir: /tmp/legacy
    command: ["echo", "hi"]
    port: 3000
    health: http://localhost:3000/ping
`
	cfg, err := Load(writeTempConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	svc := cfg.Services[0]

	// Legacy health should be mapped into HealthCheckCfg.URL
	if svc.HealthCheckCfg.URL != "http://localhost:3000/ping" {
		t.Errorf("expected health mapped to health_check.url, got %s", svc.HealthCheckCfg.URL)
	}
}

func TestHealthCheckTakesPrecedenceOverLegacy(t *testing.T) {
	yaml := `
services:
  - id: both
    name: Both
    project: proj
    dir: /tmp/both
    command: ["echo", "hi"]
    port: 3000
    health: http://localhost:3000/old
    health_check:
      url: http://localhost:3000/new
`
	cfg, err := Load(writeTempConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	svc := cfg.Services[0]

	// When health_check.url is set, it should NOT be overwritten by legacy health
	if svc.HealthCheckCfg.URL != "http://localhost:3000/new" {
		t.Errorf("expected health_check.url to take precedence, got %s", svc.HealthCheckCfg.URL)
	}
}

func TestMissingOptionalFieldsDefault(t *testing.T) {
	yaml := `
version: 1
services:
  - id: minimal
    name: Minimal
    project: proj
    dir: /tmp/min
    command: ["echo", "hi"]
    port: 5000
`
	cfg, err := Load(writeTempConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	svc := cfg.Services[0]

	if svc.AutoStart {
		t.Error("auto_start should default to false")
	}
	if svc.AutoRestart {
		t.Error("auto_restart should default to false")
	}
	if svc.RestartDelay != "" {
		t.Errorf("restart_delay should default to empty, got %s", svc.RestartDelay)
	}
	if len(svc.DependsOn) != 0 {
		t.Errorf("depends_on should default to empty, got %v", svc.DependsOn)
	}
	if len(svc.Profiles) != 0 {
		t.Errorf("profiles should default to empty, got %v", svc.Profiles)
	}
	if svc.HealthCheckCfg.URL != "" {
		t.Errorf("health_check.url should default to empty, got %s", svc.HealthCheckCfg.URL)
	}
	if len(svc.HealthCheckCfg.Command) != 0 {
		t.Errorf("health_check.command should default to empty, got %v", svc.HealthCheckCfg.Command)
	}
}

func TestTildeExpansion(t *testing.T) {
	home, _ := os.UserHomeDir()
	yaml := `
services:
  - id: tilde
    name: Tilde
    project: proj
    dir: ~/some/path
    command: ["echo"]
    port: 1234
`
	cfg, err := Load(writeTempConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	expected := filepath.Join(home, "some/path")
	if cfg.Services[0].Dir != expected {
		t.Errorf("expected dir %s, got %s", expected, cfg.Services[0].Dir)
	}
}
