package local

import "testing"

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
