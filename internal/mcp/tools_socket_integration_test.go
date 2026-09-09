package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/connector"
	dockerconn "github.com/chrispian/cerberus/internal/connector/docker"
	contract "github.com/chrispian/cerberus/pkg/connector"
	gmcp "github.com/hollis-labs/go-mcp/server"
)

// shortSocketPath returns a short unix-socket path (macOS sun_path
// limit is ~104 chars). See cerbapi/socket_test.go for the rationale.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("cerb-mcpit-%d-%d.sock", os.Getpid(), time.Now().UnixNano())
	p := filepath.Join(os.TempDir(), name)
	t.Cleanup(func() { _ = os.Remove(p) })
	return p
}

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// startDaemonSocketWithPath is the v2-config analog of startDaemonSocket:
// the InProcessClient is wired with a cfgPath so project/resource/pipeline
// endpoints reload the v2 config file on every call.
func startDaemonSocketWithPath(t *testing.T, cfgPath string) *cerbapi.SocketClient {
	t.Helper()
	sockPath := shortSocketPath(t)

	inProc := cerbapi.NewInProcessClient(cerbapi.WithConfigPath(cfgPath))
	srv := cerbapi.NewSocketServer(inProc, sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("socket server: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("socket %s never appeared", sockPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cerbapi.NewSocketClient(sockPath)
}

func startDaemonSocketWithConnectors(t *testing.T) *cerbapi.SocketClient {
	t.Helper()
	sockPath := shortSocketPath(t)

	registry := connector.NewRegistry()
	registry.RegisterDefinition(dockerconn.Definition())
	inProc := cerbapi.NewInProcessClient(
		cerbapi.WithExternalConnectorService(cerbapi.NewExternalConnectorService(registry)),
	)
	srv := cerbapi.NewSocketServer(inProc, sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("socket server: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("socket %s never appeared", sockPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cerbapi.NewSocketClient(sockPath)
}

func startDaemonSocketWithClient(t *testing.T, client cerbapi.Client) *cerbapi.SocketClient {
	t.Helper()
	sockPath := shortSocketPath(t)

	srv := cerbapi.NewSocketServer(client, sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("socket server: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("socket %s never appeared", sockPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cerbapi.NewSocketClient(sockPath)
}

type fakeSocketProgressClient struct{}

func (fakeSocketProgressClient) ResourceLogs(context.Context, string, int, string) (*cerbapi.LogLines, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) Health(context.Context, string) (*cerbapi.DaemonHealth, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) ListProjects(context.Context) ([]cerbapi.ProjectInfo, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) ListResources(context.Context, cerbapi.ResourceListArgs) ([]cerbapi.ResourceInfo, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) ResolveDiagnostics(context.Context) (*cerbapi.ResolveDiagnostics, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) GetResourceRuntime(context.Context, string) (*cerbapi.ResourceRuntimeStatus, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) GetResourceInspect(context.Context, string) (*cerbapi.ResourceInspect, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) GetResourceDoctor(context.Context, string) (*cerbapi.ResourceDoctor, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) DeployResource(ctx context.Context, _ string, _ ...cerbapi.DeployResourceOption) (*cerbapi.OpResult, error) {
	gmcp.NotifyMessage(ctx, "info", "fake deploy started")
	gmcp.NotifyProgress(ctx, "fake-deploy", 1, 2, "deploying")
	gmcp.NotifyMessage(ctx, "info", "fake deploy completed")
	return &cerbapi.OpResult{Success: true, ServiceID: "demo", Message: "deployed"}, nil
}
func (fakeSocketProgressClient) ApplyResource(ctx context.Context, _ string) (*cerbapi.OpResult, error) {
	gmcp.NotifyMessage(ctx, "info", "fake apply started")
	gmcp.NotifyProgress(ctx, "fake-apply", 1, 2, "applying")
	gmcp.NotifyMessage(ctx, "info", "fake apply completed")
	return &cerbapi.OpResult{Success: true, ServiceID: "demo", Message: "applied"}, nil
}
func (fakeSocketProgressClient) ReloadResource(context.Context, string) (*cerbapi.OpResult, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) StopResource(context.Context, string) (*cerbapi.OpResult, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) SyncResource(ctx context.Context, _ string) (*cerbapi.OpResult, error) {
	gmcp.NotifyMessage(ctx, "info", "fake sync started")
	gmcp.NotifyProgress(ctx, "fake-sync", 1, 2, "syncing")
	gmcp.NotifyMessage(ctx, "info", "fake sync completed")
	return &cerbapi.OpResult{Success: true, ServiceID: "demo", Message: "synced"}, nil
}
func (fakeSocketProgressClient) RemoveResource(context.Context, string) (*cerbapi.OpResult, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) ListPipelines(context.Context) ([]cerbapi.PipelineInfo, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) RunPipeline(ctx context.Context, _ string) (*cerbapi.PipelineRunResult, error) {
	gmcp.NotifyMessage(ctx, "info", "fake pipeline started")
	gmcp.NotifyProgress(ctx, "fake-pipeline", 1, 2, "running")
	gmcp.NotifyMessage(ctx, "info", "fake pipeline completed")
	raw, err := json.Marshal(map[string]any{
		"pipeline_id": "demo-pipeline",
		"status":      "healthy",
	})
	if err != nil {
		return nil, err
	}
	return &cerbapi.PipelineRunResult{Success: true, Raw: raw}, nil
}
func (fakeSocketProgressClient) ListConnectors(context.Context) ([]contract.Definition, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) ExecuteConnectorOperation(ctx context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	gmcp.NotifyMessage(ctx, "info", "fake connector operation started")
	gmcp.NotifyProgress(ctx, "fake-connector", 1, 2, "running connector operation")
	gmcp.NotifyMessage(ctx, "info", "fake connector operation completed")
	return cerbapi.ExternalConnectorOperationResult{
		Connector: args.Connector,
		Operation: args.Operation,
		Data:      map[string]any{"ok": true},
	}, nil
}
func (fakeSocketProgressClient) PluginHealth(ctx context.Context, _ cerbapi.PluginConnectorHealthArgs) (cerbapi.PluginConnectorHealth, error) {
	gmcp.NotifyMessage(ctx, "info", "fake plugin health started")
	gmcp.NotifyProgress(ctx, "fake-plugin-health", 1, 2, "checking plugin health")
	gmcp.NotifyMessage(ctx, "info", "fake plugin health completed")
	return cerbapi.PluginConnectorHealth{ID: "docker", Loaded: true, Healthy: true, Message: "ok"}, nil
}
func (fakeSocketProgressClient) ExecutePluginConnector(ctx context.Context, args cerbapi.PluginConnectorExecArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	gmcp.NotifyMessage(ctx, "info", "fake plugin operation started")
	gmcp.NotifyProgress(ctx, "fake-plugin-exec", 1, 2, "running plugin operation")
	gmcp.NotifyMessage(ctx, "info", "fake plugin operation completed")
	return cerbapi.ExternalConnectorOperationResult{Connector: "docker", Operation: args.Operation, Data: map[string]any{"ok": true}}, nil
}
func (fakeSocketProgressClient) InstallManagedPlugin(ctx context.Context, _ cerbapi.PluginConnectorHealthArgs) (cerbapi.ManagedPluginConnectorState, error) {
	gmcp.NotifyMessage(ctx, "info", "fake managed plugin install started")
	gmcp.NotifyProgress(ctx, "fake-managed-install", 1, 2, "installing managed plugin")
	gmcp.NotifyMessage(ctx, "info", "fake managed plugin install completed")
	return cerbapi.ManagedPluginConnectorState{ID: "docker", Loaded: false, Version: "0.1.0"}, nil
}
func (fakeSocketProgressClient) LoadManagedPlugin(ctx context.Context, id string) (cerbapi.ManagedPluginConnectorState, error) {
	gmcp.NotifyMessage(ctx, "info", "fake managed plugin load started")
	gmcp.NotifyProgress(ctx, "fake-managed-load", 1, 2, "loading managed plugin")
	gmcp.NotifyMessage(ctx, "info", "fake managed plugin load completed")
	return cerbapi.ManagedPluginConnectorState{ID: id, Loaded: true, Version: "0.1.0"}, nil
}
func (fakeSocketProgressClient) UnloadManagedPlugin(ctx context.Context, id string) (cerbapi.ManagedPluginConnectorState, error) {
	gmcp.NotifyMessage(ctx, "info", "fake managed plugin unload started")
	gmcp.NotifyProgress(ctx, "fake-managed-unload", 1, 2, "unloading managed plugin")
	gmcp.NotifyMessage(ctx, "info", "fake managed plugin unload completed")
	return cerbapi.ManagedPluginConnectorState{ID: id, Loaded: false, Version: "0.1.0"}, nil
}
func (fakeSocketProgressClient) ListManagedPlugins(context.Context) ([]cerbapi.ManagedPluginConnectorState, error) {
	return nil, errors.New("not implemented")
}
func (fakeSocketProgressClient) ManagedPluginHealth(ctx context.Context, id string) (cerbapi.PluginConnectorHealth, error) {
	gmcp.NotifyMessage(ctx, "info", "fake managed plugin health started")
	gmcp.NotifyProgress(ctx, "fake-managed-health", 1, 2, "checking managed plugin health")
	gmcp.NotifyMessage(ctx, "info", "fake managed plugin health completed")
	return cerbapi.PluginConnectorHealth{ID: id, Loaded: true, Healthy: true, Message: "ok"}, nil
}
func (fakeSocketProgressClient) ExecuteManagedPlugin(ctx context.Context, id string, args cerbapi.PluginConnectorExecArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	gmcp.NotifyMessage(ctx, "info", "fake managed plugin operation started")
	gmcp.NotifyProgress(ctx, "fake-managed-exec", 1, 2, "running managed plugin operation")
	gmcp.NotifyMessage(ctx, "info", "fake managed plugin operation completed")
	return cerbapi.ExternalConnectorOperationResult{Connector: id, Operation: args.Operation, Data: map[string]any{"ok": true}}, nil
}

// TestProjectListTool_ViaSocket_PicksUpConfigEdit is the v2-config
// analog of TestStatusTool_ViaSocket_PicksUpConfigEdit: a project
// added to the v2 config on disk must surface through a long-lived
// SocketClient on the next tool invocation — no subprocess restart,
// no tool re-construction. This closes the staleness bug class for
// project / resource / pipeline endpoints that the review flagged.
func TestProjectListTool_ViaSocket_PicksUpConfigEdit(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 2
projects:
  - id: alpha
    name: Project Alpha
  - id: bravo
    name: Project Bravo
`)

	socketClient := startDaemonSocketWithPath(t, path)

	tool := NewCerberusProjectListTool(socketClient)

	// Baseline: alpha + bravo via the socket round-trip. List tools return a
	// compact-JSON budgeted envelope (`"id":"alpha"`, no space after colon),
	// distinct from the indented format used by single-record tools.
	out, err := tool.Handler(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id":"alpha"`) || !strings.Contains(out, `"id":"bravo"`) {
		t.Fatalf("baseline missing expected projects: %s", out)
	}
	if strings.Contains(out, `"id":"charlie"`) {
		t.Fatalf("baseline should not have charlie: %s", out)
	}

	// Edit config on disk: add charlie, remove alpha. No daemon
	// restart, no tool re-construction.
	if werr := os.WriteFile(path, []byte(`
version: 2
projects:
  - id: bravo
    name: Project Bravo
  - id: charlie
    name: Project Charlie
`), 0600); werr != nil {
		t.Fatal(werr)
	}

	// Next call must reflect both the addition AND the removal.
	out, err = tool.Handler(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id":"charlie"`) {
		t.Fatalf("post-edit: project_list did not pick up charlie: %s", out)
	}
	if strings.Contains(out, `"id":"alpha"`) {
		t.Fatalf("post-edit: project_list still shows removed alpha (stale snapshot): %s", out)
	}
}

func TestResourceStatusTool_ViaSocket_ReturnsRuntimeMetadata(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd-specific resource metadata test")
	}
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "volon-api"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	path := writeConfig(t, dir, `
version: 2
projects:
  - id: volon
    name: Volon
resources:
  - id: volon-api
    name: Volon API
    type: process
    project: volon
    connector: local
    config:
      dir: `+workspace+`
      command: ["./volon-api", "serve"]
      mode: os_service
      supervisor: launchd
      run_from: artifact
`)

	socketClient := startDaemonSocketWithPath(t, path)
	tool := NewCerberusResourceStatusTool(socketClient)

	out, err := tool.Handler(context.Background(), map[string]interface{}{"resource_id": "volon-api"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id": "volon-api"`) {
		t.Fatalf("missing resource id: %s", out)
	}
	if !strings.Contains(out, `"mode": "os_service"`) {
		t.Fatalf("missing mode: %s", out)
	}
	if !strings.Contains(out, `"supervisor": "launchd"`) {
		t.Fatalf("missing supervisor: %s", out)
	}
}

func TestPipelineRunTool_ViaSocket_EmitsBridgeProgress(t *testing.T) {
	socketClient := startDaemonSocketWithClient(t, fakeSocketProgressClient{})
	tool := NewCerberusPipelineRunTool(socketClient)

	var notifications []gmcp.Notification
	ctx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) {
		notifications = append(notifications, n)
	})

	out, err := tool.Handler(ctx, map[string]interface{}{"pipeline_id": "demo-pipeline"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"demo-pipeline"`) {
		t.Fatalf("output = %s, want demo-pipeline", out)
	}
	if len(notifications) < 2 {
		t.Fatalf("notifications len = %d, want at least 2", len(notifications))
	}
	body, err := json.Marshal(notifications)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "fake pipeline started") {
		t.Fatalf("notifications = %s, want daemon-streamed start message", string(body))
	}
	if !strings.Contains(string(body), "fake pipeline completed") {
		t.Fatalf("notifications = %s, want daemon-streamed completion message", string(body))
	}
}

func TestResourceDeployTool_ViaSocket_EmitsBridgeProgress(t *testing.T) {
	socketClient := startDaemonSocketWithClient(t, fakeSocketProgressClient{})
	tool := NewCerberusResourceDeployTool(socketClient)

	var notifications []gmcp.Notification
	ctx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) {
		notifications = append(notifications, n)
	})

	out, err := tool.Handler(ctx, map[string]interface{}{"resource_id": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"success": true`) {
		t.Fatalf("output = %s, want success", out)
	}
	body, err := json.Marshal(notifications)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "fake deploy started") {
		t.Fatalf("notifications = %s, want daemon-streamed start message", string(body))
	}
	if !strings.Contains(string(body), "fake deploy completed") {
		t.Fatalf("notifications = %s, want daemon-streamed completion message", string(body))
	}
}

func TestResourceApplyTool_ViaSocket_EmitsBridgeProgress(t *testing.T) {
	socketClient := startDaemonSocketWithClient(t, fakeSocketProgressClient{})
	tool := NewCerberusResourceApplyTool(socketClient)

	var notifications []gmcp.Notification
	ctx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) {
		notifications = append(notifications, n)
	})

	out, err := tool.Handler(ctx, map[string]interface{}{"resource_id": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"success": true`) {
		t.Fatalf("output = %s, want success", out)
	}
	body, err := json.Marshal(notifications)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "fake apply started") {
		t.Fatalf("notifications = %s, want daemon-streamed start message", string(body))
	}
	if !strings.Contains(string(body), "fake apply completed") {
		t.Fatalf("notifications = %s, want daemon-streamed completion message", string(body))
	}
}

func TestResourceSyncTool_ViaSocket_EmitsBridgeProgress(t *testing.T) {
	socketClient := startDaemonSocketWithClient(t, fakeSocketProgressClient{})
	tool := NewCerberusResourceSyncTool(socketClient)

	var notifications []gmcp.Notification
	ctx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) {
		notifications = append(notifications, n)
	})

	out, err := tool.Handler(ctx, map[string]interface{}{"resource_id": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"success": true`) {
		t.Fatalf("output = %s, want success", out)
	}
	body, err := json.Marshal(notifications)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "fake sync started") {
		t.Fatalf("notifications = %s, want daemon-streamed start message", string(body))
	}
	if !strings.Contains(string(body), "fake sync completed") {
		t.Fatalf("notifications = %s, want daemon-streamed completion message", string(body))
	}
}

func TestPipelineRunTool_ViaSocket_StreamsRealDaemonProgress(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 2
pipelines:
  - id: smoke-pipeline
    name: Smoke Pipeline
    stages:
      - name: smoke
        actions:
          - type: shell
            command: "printf smoke-ok"
`)

	socketClient := startDaemonSocketWithPath(t, path)
	tool := NewCerberusPipelineRunTool(socketClient)

	var notifications []gmcp.Notification
	ctx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) {
		notifications = append(notifications, n)
	})

	out, err := tool.Handler(ctx, map[string]interface{}{"pipeline_id": "smoke-pipeline"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"pipeline_id":"smoke-pipeline"`) {
		t.Fatalf("output = %s, want smoke-pipeline result", out)
	}
	body, err := json.Marshal(notifications)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Starting pipeline smoke-pipeline (1 stage(s))") {
		t.Fatalf("notifications = %s, want executor start message", string(body))
	}
	if !strings.Contains(string(body), "Stage smoke started") {
		t.Fatalf("notifications = %s, want stage start message", string(body))
	}
	if !strings.Contains(string(body), "Pipeline smoke-pipeline completed") {
		t.Fatalf("notifications = %s, want executor completion message", string(body))
	}
}

func TestConnectorExec_ViaSocket_StreamsProgress(t *testing.T) {
	socketClient := startDaemonSocketWithClient(t, fakeSocketProgressClient{})

	var notifications []gmcp.Notification
	ctx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) {
		notifications = append(notifications, n)
	})

	_, err := socketClient.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
		Connector: "docker",
		Operation: "logs",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(notifications)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "fake connector operation started") {
		t.Fatalf("notifications = %s, want connector start message", string(body))
	}
	if !strings.Contains(string(body), "fake connector operation completed") {
		t.Fatalf("notifications = %s, want connector completion message", string(body))
	}
}

func TestManagedPluginLoad_ViaSocket_StreamsProgress(t *testing.T) {
	socketClient := startDaemonSocketWithClient(t, fakeSocketProgressClient{})

	var notifications []gmcp.Notification
	ctx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) {
		notifications = append(notifications, n)
	})

	_, err := socketClient.LoadManagedPlugin(ctx, "docker")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(notifications)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "fake managed plugin load started") {
		t.Fatalf("notifications = %s, want managed plugin load start", string(body))
	}
	if !strings.Contains(string(body), "fake managed plugin load completed") {
		t.Fatalf("notifications = %s, want managed plugin load completion", string(body))
	}
}

func TestConnectorListToolViaSocket(t *testing.T) {
	socketClient := startDaemonSocketWithConnectors(t)
	tool := NewCerberusConnectorListTool(socketClient)

	out, err := tool.Handler(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	// List tools return a compact-JSON budgeted envelope; assertion uses the
	// no-space-after-colon format to match.
	if !strings.Contains(out, `"id":"docker"`) {
		t.Fatalf("missing docker connector definition: %s", out)
	}
	if !strings.Contains(out, `"resource_types"`) {
		t.Fatalf("missing resource types: %s", out)
	}
}

func TestConnectorDescribeToolViaSocket(t *testing.T) {
	socketClient := startDaemonSocketWithConnectors(t)
	tool := NewCerberusConnectorDescribeTool(socketClient)

	out, err := tool.Handler(context.Background(), map[string]interface{}{"id": "docker"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id": "docker"`) {
		t.Fatalf("missing docker connector definition: %s", out)
	}
	if !strings.Contains(out, `"operations"`) {
		t.Fatalf("missing operations: %s", out)
	}
}
