package local

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/domain"
)

func TestDevSessionBackendRequiresService(t *testing.T) {
	backend := devSessionBackend{}
	_, err := backend.Status(context.Background(), &domain.Resource{ID: "svc"}, ProcessSpec{}, nil)
	if err == nil || !strings.Contains(err.Error(), "requires a session") {
		t.Fatalf("expected session error, got %v", err)
	}
}

// Auto resolves to the platform's own supervisor, and each is asked for the
// unit's real state: an uninstalled service is stopped, never (unknown, nil).
func TestOSServiceBackendStatusAutoSupervisor(t *testing.T) {
	home := t.TempDir()
	runner := &fakeCommandRunner{out: map[string][]byte{}, err: map[string]error{}}
	setLaunchdPrintNotFound(runner, "com.hollis-labs.cerberus.default.svc")
	runner.out["systemctl --user show com.hollis-labs.cerberus.default.svc.service --property "+strings.Join(systemdShowProperties, ",")] = []byte("LoadState=not-found\nActiveState=inactive\nSubState=dead\n")
	backend := osServiceBackend{
		launchd: launchdBackend{runner: runner, homeDir: func() (string, error) { return home, nil }, uid: func() int { return 501 }},
		systemd: systemdBackend{runner: runner, homeDir: func() (string, error) { return home, nil }, lookPath: bareToolName},
	}
	state, err := backend.Status(context.Background(), &domain.Resource{ID: "svc"}, ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorAuto,
	}, nil)
	switch runtime.GOOS {
	case "darwin", "linux":
		if err != nil {
			t.Fatalf("unexpected error on %s: %v", runtime.GOOS, err)
		}
		if state != domain.StateStopped {
			t.Fatalf("state = %q, want %q", state, domain.StateStopped)
		}
		want := map[string]string{"darwin": "launchctl", "linux": "systemctl"}[runtime.GOOS]
		if len(runner.calls) == 0 || runner.calls[0].name != want {
			t.Fatalf("calls = %+v, want %s", runner.calls, want)
		}
	default:
		if err == nil {
			t.Fatalf("expected an error for an os_service with no supervisor implemented on %s", runtime.GOOS)
		}
	}
}

func TestConnectorBackendResolution(t *testing.T) {
	c := New()

	devRes := &domain.Resource{
		ID:        "dev",
		Connector: "local",
		Config: map[string]any{
			"dir":     "/tmp/dev",
			"command": []string{"echo", "dev"},
		},
	}
	backend, spec, svc, err := c.runtimeFor(devRes)
	if err != nil {
		t.Fatalf("runtimeFor(dev) failed: %v", err)
	}
	if spec.Mode != ProcessModeDevSession {
		t.Fatalf("spec.Mode = %q, want %q", spec.Mode, ProcessModeDevSession)
	}
	if _, ok := backend.(devSessionBackend); !ok {
		t.Fatalf("backend type = %T, want devSessionBackend", backend)
	}
	if svc == nil {
		t.Fatal("dev_session should create a session")
	}

	osRes := &domain.Resource{
		ID:        "svc",
		Connector: "local",
		Config: map[string]any{
			"dir":        "/tmp/svc",
			"command":    []string{"./svc"},
			"mode":       "os_service",
			"supervisor": "launchd",
		},
	}
	backend, spec, svc, err = c.runtimeFor(osRes)
	if err != nil {
		t.Fatalf("runtimeFor(os_service) failed: %v", err)
	}
	if spec.Mode != ProcessModeOSService {
		t.Fatalf("spec.Mode = %q, want %q", spec.Mode, ProcessModeOSService)
	}
	if _, ok := backend.(osServiceBackend); !ok {
		t.Fatalf("backend type = %T, want osServiceBackend", backend)
	}
	if svc != nil {
		t.Fatal("os_service should not allocate a managed service in the connector runtime path")
	}
}
