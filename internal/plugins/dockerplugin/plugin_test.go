package dockerplugin

import (
	"context"
	"encoding/json"
	"testing"

	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	plugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

type fakeBackend struct {
	containers []dockerconn.Container
	logs       string
	state      *dockerconn.Container

	started   string
	stopped   string
	removed   string
	composeUp string
}

func (f *fakeBackend) WithTarget(dockerconn.Target) dockerconn.Backend { return f }

func (f *fakeBackend) ListContainers(context.Context) ([]dockerconn.Container, error) {
	return f.containers, nil
}

func (f *fakeBackend) ContainerStatus(context.Context, string) (*dockerconn.Container, error) {
	return f.state, nil
}

func (f *fakeBackend) StartContainer(_ context.Context, nameOrID string) error {
	f.started = nameOrID
	return nil
}

func (f *fakeBackend) StopContainer(_ context.Context, nameOrID string) error {
	f.stopped = nameOrID
	return nil
}

func (f *fakeBackend) RemoveContainer(_ context.Context, nameOrID string) error {
	f.removed = nameOrID
	return nil
}

func (f *fakeBackend) ContainerLogs(context.Context, string, int) (string, error) {
	return f.logs, nil
}

func (f *fakeBackend) ComposeUp(_ context.Context, composeFile string) error {
	f.composeUp = composeFile
	return nil
}

func (f *fakeBackend) ComposeDown(context.Context, string) error { return nil }
func (f *fakeBackend) ComposePS(context.Context, string) (*dockerconn.ComposeStack, error) {
	return nil, nil
}

func TestPluginMCPLogs(t *testing.T) {
	p := NewWithConnector(dockerconn.NewWithBackend(&fakeBackend{logs: "hello"}))
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	result, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName:  plugin.ToolNameForOperation("docker", "logs"),
		Arguments: map[string]interface{}{"container": "web", "lines": float64(10)},
	})
	if err != nil {
		t.Fatalf("MCPCallTool: %v", err)
	}

	var logs string
	if err := json.Unmarshal(result.Content, &logs); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if logs != "hello" {
		t.Fatalf("logs = %q, want hello", logs)
	}
}

func TestPluginMCPStartCompose(t *testing.T) {
	backend := &fakeBackend{}
	p := NewWithConnector(dockerconn.NewWithBackend(backend))
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName: plugin.ToolNameForOperation("docker", "start"),
		Arguments: map[string]interface{}{
			"compose_file": "docker-compose.yml",
		},
	}); err != nil {
		t.Fatalf("MCPCallTool: %v", err)
	}
	if backend.composeUp != "docker-compose.yml" {
		t.Fatalf("composeUp = %q, want docker-compose.yml", backend.composeUp)
	}
}

func TestPluginMCPStatus(t *testing.T) {
	backend := &fakeBackend{state: &dockerconn.Container{State: "running"}}
	p := NewWithConnector(dockerconn.NewWithBackend(backend))
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	result, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName:  plugin.ToolNameForOperation("docker", "status"),
		Arguments: map[string]interface{}{"container": "web"},
	})
	if err != nil {
		t.Fatalf("MCPCallTool: %v", err)
	}

	var state string
	if err := json.Unmarshal(result.Content, &state); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if state != "running" {
		t.Fatalf("state = %q, want running", state)
	}
}
