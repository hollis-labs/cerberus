package local

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/service"
)

func TestDevSessionBackendRequiresService(t *testing.T) {
	backend := devSessionBackend{}
	_, err := backend.Status(context.Background(), &domain.Resource{ID: "svc"}, ProcessSpec{}, nil)
	if err == nil || !strings.Contains(err.Error(), "requires a managed service") {
		t.Fatalf("expected managed-service error, got %v", err)
	}
}

func TestOSServiceBackendStatusAutoSupervisor(t *testing.T) {
	backend := osServiceBackend{}
	state, err := backend.Status(context.Background(), &domain.Resource{ID: "svc"}, ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorAuto,
	}, nil)
	if runtime.GOOS == "darwin" {
		if err != nil {
			t.Fatalf("unexpected error on darwin: %v", err)
		}
		if state != domain.StateStopped {
			t.Fatalf("state = %q, want %q", state, domain.StateStopped)
		}
		return
	}
	if err == nil {
		t.Fatal("expected non-darwin auto supervisor resolution to be unsupported in current scope")
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
		t.Fatal("dev_session should create a managed service")
	}

	svc = &service.ManagedService{}
	_ = svc

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
