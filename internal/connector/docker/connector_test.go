package docker

import (
	"context"
	"testing"

	"github.com/hollis-labs/cerberus/internal/domain"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

type fakeBackend struct {
	started  string
	stopped  string
	removed  string
	upFile   string
	downFile string

	container *Container
	stack     *ComposeStack
}

func (b *fakeBackend) ListContainers(_ context.Context) ([]Container, error) {
	return nil, nil
}

func (b *fakeBackend) ContainerStatus(_ context.Context, _ string) (*Container, error) {
	return b.container, nil
}

func (b *fakeBackend) StartContainer(_ context.Context, nameOrID string) error {
	b.started = nameOrID
	return nil
}

func (b *fakeBackend) StopContainer(_ context.Context, nameOrID string) error {
	b.stopped = nameOrID
	return nil
}

func (b *fakeBackend) RemoveContainer(_ context.Context, nameOrID string) error {
	b.removed = nameOrID
	return nil
}

func (b *fakeBackend) ContainerLogs(_ context.Context, _ string, _ int) (string, error) {
	return "", nil
}

func (b *fakeBackend) ComposeUp(_ context.Context, composeFile string) error {
	b.upFile = composeFile
	return nil
}

func (b *fakeBackend) ComposeDown(_ context.Context, composeFile string) error {
	b.downFile = composeFile
	return nil
}

func (b *fakeBackend) ComposePS(_ context.Context, _ string) (*ComposeStack, error) {
	return b.stack, nil
}

func TestDockerConnectorContainerLifecycleUsesResourceConfig(t *testing.T) {
	backend := &fakeBackend{container: &Container{State: "running"}}
	conn := NewWithBackend(backend)
	res := &domain.Resource{
		ID:     "fallback",
		Config: map[string]any{"container": "web"},
	}

	if err := conn.Start(context.Background(), res); err != nil {
		t.Fatalf("start: %v", err)
	}
	if backend.started != "web" {
		t.Fatalf("started = %q, want web", backend.started)
	}

	state, err := conn.Status(context.Background(), res)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if state != domain.StateRunning {
		t.Fatalf("state = %q, want %q", state, domain.StateRunning)
	}

	if err := conn.Destroy(context.Background(), res); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if backend.removed != "web" {
		t.Fatalf("removed = %q, want web", backend.removed)
	}
}

func TestDockerConnectorComposeLifecycleUsesComposeFile(t *testing.T) {
	backend := &fakeBackend{
		stack: &ComposeStack{
			Services: []ComposeService{
				{Name: "api", State: "running"},
				{Name: "worker", State: "exited"},
			},
		},
	}
	conn := NewWithBackend(backend)
	res := &domain.Resource{Config: map[string]any{"compose_file": "docker-compose.yml"}}

	if err := conn.Start(context.Background(), res); err != nil {
		t.Fatalf("start: %v", err)
	}
	if backend.upFile != "docker-compose.yml" {
		t.Fatalf("upFile = %q, want docker-compose.yml", backend.upFile)
	}

	state, err := conn.Status(context.Background(), res)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if state != domain.StateStarting {
		t.Fatalf("state = %q, want %q", state, domain.StateStarting)
	}

	if err := conn.Stop(context.Background(), res); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if backend.downFile != "docker-compose.yml" {
		t.Fatalf("downFile = %q, want docker-compose.yml", backend.downFile)
	}
}

func TestDockerConnectorDefinition(t *testing.T) {
	conn := NewWithBackend(&fakeBackend{})
	def := conn.Definition()

	if def.ID != "docker" {
		t.Fatalf("ID = %q, want docker", def.ID)
	}
	if len(def.ResourceTypes) != 1 || def.ResourceTypes[0] != "container" {
		t.Fatalf("ResourceTypes = %#v, want [container]", def.ResourceTypes)
	}
	if !def.Capabilities.CanDestroy || !def.Capabilities.CanLogs || !def.Capabilities.CanHealth {
		t.Fatalf("Capabilities = %#v, want destroy/logs/health", def.Capabilities)
	}
	if len(def.Operations) == 0 {
		t.Fatal("expected operations")
	}

	var _ contract.Describer = conn
}
