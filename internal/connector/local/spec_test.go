package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSpecFromResourceConfigDefaults(t *testing.T) {
	spec, err := SpecFromResourceConfig(map[string]any{
		"dir":     "/tmp/app",
		"command": []string{"go", "run", "."},
	})
	if err != nil {
		t.Fatalf("SpecFromResourceConfig failed: %v", err)
	}

	if spec.Mode != ProcessModeDevSession {
		t.Fatalf("Mode = %q, want %q", spec.Mode, ProcessModeDevSession)
	}
	if spec.Supervisor != ProcessSupervisorAuto {
		t.Fatalf("Supervisor = %q, want %q", spec.Supervisor, ProcessSupervisorAuto)
	}
	if spec.RunFrom != ProcessRunFromWorkspace {
		t.Fatalf("RunFrom = %q, want %q", spec.RunFrom, ProcessRunFromWorkspace)
	}
	if !spec.InstallAfterBuild {
		t.Fatalf("InstallAfterBuild = false, want true (documented default when key absent)")
	}
}

func TestSpecInstallAfterBuildRoundTripPreservesAbsence(t *testing.T) {
	// Absent install_after_build in YAML → parsed as true (default) →
	// ToResourceConfig must NOT re-emit the key, so a subsequent re-parse
	// still sees absence and the resolver still falls through to the
	// global default. This is the round-trip flip that previously caused
	// implicit-default resources to ossify as explicit opt-outs.
	spec, err := SpecFromResourceConfig(map[string]any{
		"dir":     "/tmp/app",
		"command": []string{"./app"},
	})
	if err != nil {
		t.Fatalf("SpecFromResourceConfig failed: %v", err)
	}
	cfg := spec.ToResourceConfig()
	if _, present := cfg["install_after_build"]; present {
		t.Fatalf("ToResourceConfig should omit install_after_build for default-on spec; got cfg[%q]=%v", "install_after_build", cfg["install_after_build"])
	}
}

func TestSpecInstallAfterBuildExplicitFalseRoundTrips(t *testing.T) {
	// Explicit false in YAML round-trips: parsed as false → ToResourceConfig
	// emits false → re-parse sees false. This is the explicit-opt-out path
	// that must survive serialization unchanged.
	spec, err := SpecFromResourceConfig(map[string]any{
		"dir":                 "/tmp/app",
		"command":             []string{"./app"},
		"install_after_build": false,
	})
	if err != nil {
		t.Fatalf("SpecFromResourceConfig failed: %v", err)
	}
	if spec.InstallAfterBuild {
		t.Fatalf("InstallAfterBuild = true after parsing explicit false")
	}
	cfg := spec.ToResourceConfig()
	emitted, present := cfg["install_after_build"]
	if !present {
		t.Fatalf("ToResourceConfig should emit explicit install_after_build:false; got missing")
	}
	if b, ok := emitted.(bool); !ok || b {
		t.Fatalf("ToResourceConfig emitted install_after_build=%v (type %T), want bool(false)", emitted, emitted)
	}
}

func TestSpecFromResourceConfigDecodesDaemonFields(t *testing.T) {
	spec, err := SpecFromResourceConfig(map[string]any{
		"dir":              "/tmp/app",
		"command":          []any{"./cerberus", "serve"},
		"mode":             "os_service",
		"supervisor":       "launchd",
		"run_from":         "artifact",
		"service_name":     "com.example.cerberus.app",
		"artifact_path":    "/Users/chrispian/.cerberus/apps/app/bin/app",
		"install_root":     "/Users/chrispian/.cerberus/apps/app",
		"install_work_dir": "/Users/chrispian/.cerberus/apps/app/current",
		"health_check": map[string]any{
			"url":      "http://127.0.0.1:8080/health",
			"command":  []any{"curl", "-f", "http://127.0.0.1:8080/health"},
			"interval": "10s",
			"timeout":  "3s",
		},
	})
	if err != nil {
		t.Fatalf("SpecFromResourceConfig failed: %v", err)
	}

	if spec.Mode != ProcessModeOSService {
		t.Fatalf("Mode = %q, want %q", spec.Mode, ProcessModeOSService)
	}
	if spec.Supervisor != ProcessSupervisorLaunchd {
		t.Fatalf("Supervisor = %q, want %q", spec.Supervisor, ProcessSupervisorLaunchd)
	}
	if spec.RunFrom != ProcessRunFromArtifact {
		t.Fatalf("RunFrom = %q, want %q", spec.RunFrom, ProcessRunFromArtifact)
	}
	if spec.ServiceName != "com.example.cerberus.app" {
		t.Fatalf("ServiceName = %q", spec.ServiceName)
	}
	if spec.HealthCheck.URL != "http://127.0.0.1:8080/health" {
		t.Fatalf("HealthCheck.URL = %q", spec.HealthCheck.URL)
	}
	if len(spec.HealthCheck.Command) != 3 {
		t.Fatalf("HealthCheck.Command len = %d, want 3", len(spec.HealthCheck.Command))
	}
}

func TestProcessSpecToResourceConfigOmitsDefaultDaemonFields(t *testing.T) {
	spec := ProcessSpec{
		Dir:     "/tmp/app",
		Command: []string{"go", "run", "."},
	}

	cfg := spec.ToResourceConfig()

	for _, key := range []string{"mode", "supervisor", "run_from", "service_name", "artifact_path", "install_root", "install_work_dir"} {
		if _, ok := cfg[key]; ok {
			t.Fatalf("default config should omit %q", key)
		}
	}
}

func TestProcessSpecRoundTripDaemonFields(t *testing.T) {
	original := ProcessSpec{
		Dir:            "/tmp/app",
		Command:        []string{"./app", "serve"},
		Port:           8080,
		Mode:           ProcessModeOSService,
		Supervisor:     ProcessSupervisorLaunchd,
		RunFrom:        ProcessRunFromArtifact,
		ServiceName:    "com.example.app",
		ArtifactPath:   "/Users/chrispian/.cerberus/apps/app/bin/app",
		InstallRoot:    "/Users/chrispian/.cerberus/apps/app",
		InstallWorkDir: "/Users/chrispian/.cerberus/apps/app/current",
	}

	cfg := original.ToResourceConfig()
	restored, err := SpecFromResourceConfig(cfg)
	if err != nil {
		t.Fatalf("SpecFromResourceConfig failed: %v", err)
	}

	if restored.Mode != original.Mode {
		t.Fatalf("Mode = %q, want %q", restored.Mode, original.Mode)
	}
	if restored.Supervisor != original.Supervisor {
		t.Fatalf("Supervisor = %q, want %q", restored.Supervisor, original.Supervisor)
	}
	if restored.RunFrom != original.RunFrom {
		t.Fatalf("RunFrom = %q, want %q", restored.RunFrom, original.RunFrom)
	}
	if restored.ArtifactPath != original.ArtifactPath {
		t.Fatalf("ArtifactPath = %q, want %q", restored.ArtifactPath, original.ArtifactPath)
	}
}

func TestSpecFromResourceConfigRejectsLegacyBuild(t *testing.T) {
	_, err := SpecFromResourceConfig(map[string]any{
		"dir":     "/tmp/app",
		"command": []string{"./app"},
		"build":   []string{"go", "build", "."},
	})
	if err == nil {
		t.Fatalf("SpecFromResourceConfig should reject legacy build")
	}
}

func TestProcessSpecBuildStrategyRoundTrip(t *testing.T) {
	original := ProcessSpec{
		Dir:     "/tmp/app",
		Command: []string{"./app", "serve"},
		BuildStrategy: &BuildStrategyConfig{
			Kind: "go_standard",
			Source: map[string]any{
				"root": ".",
			},
			Rules: map[string]any{
				"output": "app",
				"target": "./cmd/app",
			},
		},
	}

	cfg := original.ToResourceConfig()
	if _, present := cfg["build"]; present {
		t.Fatalf("ToResourceConfig emitted legacy build key")
	}
	if _, present := cfg["build_strategy"]; !present {
		t.Fatalf("ToResourceConfig omitted build_strategy")
	}
	restored, err := SpecFromResourceConfig(cfg)
	if err != nil {
		t.Fatalf("SpecFromResourceConfig failed: %v", err)
	}
	if restored.BuildStrategy == nil || restored.BuildStrategy.Kind != "go_standard" {
		t.Fatalf("BuildStrategy = %#v, want go_standard", restored.BuildStrategy)
	}
	if got, want := restored.BuildStrategy.Rules["target"], "./cmd/app"; got != want {
		t.Fatalf("BuildStrategy.Rules[target] = %q, want %q", got, want)
	}
}

func TestSpecFromResourceConfigExpandsHomePaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir failed: %v", err)
	}
	spec, err := SpecFromResourceConfig(map[string]any{
		"dir":     "~/Projects-apps/example",
		"command": []any{"~/bin/example", "serve", "--root", "~/.example"},
		"build_strategy": map[string]any{
			"kind": "go_standard",
			"source": map[string]any{
				"root": "~/Projects-apps/example",
			},
			"rules": map[string]any{
				"output": "~/bin/example",
				"target": "./cmd/example",
			},
		},
		"env_file":         "~/.env.example",
		"log_file":         "~/.cerberus/logs/example.log",
		"artifact_path":    "~/.cerberus/apps/example/bin/example",
		"install_root":     "~/.cerberus/apps/example",
		"install_work_dir": "~/.cerberus/apps/example/current",
		"env": map[string]any{
			"APP_ROOT": "~/.example",
		},
		"health_check": map[string]any{
			"command": []any{"~/bin/healthcheck-example"},
		},
	})
	if err != nil {
		t.Fatalf("SpecFromResourceConfig failed: %v", err)
	}

	if got, want := spec.Dir, filepath.Join(home, "Projects-apps/example"); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
	if got, want := spec.Command[0], filepath.Join(home, "bin/example"); got != want {
		t.Fatalf("Command[0] = %q, want %q", got, want)
	}
	if got, want := spec.Command[2], "--root"; got != want {
		t.Fatalf("Command[2] = %q, want %q", got, want)
	}
	if got, want := spec.Command[3], filepath.Join(home, ".example"); got != want {
		t.Fatalf("Command[3] = %q, want %q", got, want)
	}
	if got, want := spec.Env["APP_ROOT"], filepath.Join(home, ".example"); got != want {
		t.Fatalf("Env[APP_ROOT] = %q, want %q", got, want)
	}
	if got, want := spec.HealthCheck.Command[0], filepath.Join(home, "bin/healthcheck-example"); got != want {
		t.Fatalf("HealthCheck.Command[0] = %q, want %q", got, want)
	}
	if got, want := spec.BuildStrategy.Source["root"], filepath.Join(home, "Projects-apps/example"); got != want {
		t.Fatalf("BuildStrategy.Source[root] = %q, want %q", got, want)
	}
	if got, want := spec.BuildStrategy.Rules["output"], filepath.Join(home, "bin/example"); got != want {
		t.Fatalf("BuildStrategy.Rules[output] = %q, want %q", got, want)
	}
}
