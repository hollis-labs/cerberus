package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/service"
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

// startDaemonSocket boots a cerbapi.SocketServer in the background and
// returns a connected SocketClient. Mirrors the daemon boot sequence
// minus the actual daemon process — enough to exercise MCP tools as if
// they were running in the standalone `cerberus mcp` subprocess.
func startDaemonSocket(t *testing.T, reg *service.ServiceRegistry, cfg *config.ConfigV2) *cerbapi.SocketClient {
	t.Helper()
	sockPath := shortSocketPath(t)

	inProc := cerbapi.NewInProcessClient(reg, cerbapi.WithConfigV2(cfg))
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

// TestStatusTool_ViaSocket_PicksUpConfigEdit is the CERB-2 acceptance
// test at the MCP layer: the tool handler is backed by a SocketClient
// (same as the standalone `cerberus mcp` subprocess). A config edit
// on the daemon side must surface through the tool without any
// subprocess restart.
func TestStatusTool_ViaSocket_PicksUpConfigEdit(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)

	reg, err := service.NewServiceRegistry(config.NewFileSource(path), nil)
	if err != nil {
		t.Fatal(err)
	}

	socketClient := startDaemonSocket(t, reg, nil)

	tool := NewCerberusStatusTool(socketClient)

	// Baseline via socket.
	out, err := tool.Handler(map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id": "one"`) {
		t.Fatalf("baseline missing service one: %s", out)
	}
	if strings.Contains(out, `"id": "two"`) {
		t.Fatalf("baseline should not have service two: %s", out)
	}

	// Edit config on disk. The daemon's registry reloads on every
	// inbound RPC so the next tool call via the same long-lived
	// SocketClient sees the new service.
	if werr := os.WriteFile(path, []byte(`
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
  - id: two
    name: Two
    dir: /tmp/two
    command: ["echo", "two"]
`), 0600); werr != nil {
		t.Fatal(werr)
	}

	out, err = tool.Handler(map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id": "two"`) {
		t.Fatalf("post-edit: tool (backed by SocketClient) did not pick up service two: %s", out)
	}
}

// TestRebuildTool_ViaSocket_DaemonUnreachable_ReturnsCleanError verifies
// the spec's "daemon not running" shape: a lifecycle tool backed by a
// SocketClient whose daemon is offline returns a structured error
// (no silent fallback to config.Load() — that would recreate the
// CERB-2 bug).
func TestRebuildTool_ViaSocket_DaemonUnreachable_ReturnsCleanError(t *testing.T) {
	// Point the client at a socket that will never exist.
	missing := shortSocketPath(t) + ".never"
	socketClient := cerbapi.NewSocketClient(missing)

	tool := NewCerberusStatusTool(socketClient)
	_, err := tool.Handler(map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error when daemon is unreachable")
	}
	if !strings.Contains(err.Error(), "daemon not running") {
		t.Fatalf("expected operator-facing 'daemon not running' hint, got: %s", err.Error())
	}
}

// startDaemonSocketWithPath is the v2-config analog of startDaemonSocket:
// the InProcessClient is wired with a cfgPath so project/resource/pipeline
// endpoints reload the v2 config file on every call — proving the
// CERB-2 staleness fix extends to the v2-config-backed endpoints, not
// just the service registry.
func startDaemonSocketWithPath(t *testing.T, reg *service.ServiceRegistry, cfgPath string) *cerbapi.SocketClient {
	t.Helper()
	sockPath := shortSocketPath(t)

	inProc := cerbapi.NewInProcessClient(reg, cerbapi.WithConfigPath(cfgPath))
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

// TestProjectListTool_ViaSocket_PicksUpConfigEdit is the v2-config
// analog of TestStatusTool_ViaSocket_PicksUpConfigEdit: a project
// added to the v2 config on disk must surface through a long-lived
// SocketClient on the next tool invocation — no subprocess restart,
// no tool re-construction. This closes the staleness bug class for
// project / resource / pipeline endpoints that the review flagged.
func TestProjectListTool_ViaSocket_PicksUpConfigEdit(t *testing.T) {
	dir := t.TempDir()
	// A single v2 config file drives both the (empty) registry and
	// the project/resource/pipeline endpoints. v2 has no `services`
	// key, so the registry starts empty — that's fine, this test
	// exercises the v2-config path only.
	path := writeConfig(t, dir, `
version: 2
projects:
  - id: alpha
    name: Project Alpha
  - id: bravo
    name: Project Bravo
`)

	reg, err := service.NewServiceRegistry(config.NewFileSource(path), nil)
	if err != nil {
		t.Fatal(err)
	}

	socketClient := startDaemonSocketWithPath(t, reg, path)

	tool := NewCerberusProjectListTool(socketClient)

	// Baseline: alpha + bravo via the socket round-trip.
	out, err := tool.Handler(map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id": "alpha"`) || !strings.Contains(out, `"id": "bravo"`) {
		t.Fatalf("baseline missing expected projects: %s", out)
	}
	if strings.Contains(out, `"id": "charlie"`) {
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
	out, err = tool.Handler(map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id": "charlie"`) {
		t.Fatalf("post-edit: project_list did not pick up charlie: %s", out)
	}
	if strings.Contains(out, `"id": "alpha"`) {
		t.Fatalf("post-edit: project_list still shows removed alpha (stale snapshot): %s", out)
	}
}

func TestResourceStatusTool_ViaSocket_ReturnsRuntimeMetadata(t *testing.T) {
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

	reg, err := service.NewServiceRegistry(config.NewFileSource(path), nil)
	if err != nil {
		t.Fatal(err)
	}
	socketClient := startDaemonSocketWithPath(t, reg, path)
	tool := NewCerberusResourceStatusTool(socketClient)

	out, err := tool.Handler(map[string]interface{}{"resource_id": "volon-api"})
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

// TestMultipleSubprocesses_SeeSameConfigEdit simulates the 3-subprocess
// scenario from the CERB-2 manual validation plan: three long-lived
// SocketClients (all connected to the same daemon) each observe a
// config edit at the daemon without any subprocess restart.
func TestMultipleSubprocesses_SeeSameConfigEdit(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)

	reg, err := service.NewServiceRegistry(config.NewFileSource(path), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Three independent SocketClients against the same daemon — each
	// simulates a separate Claude Code MCP subprocess.
	c1 := startDaemonSocket(t, reg, nil)
	// Reuse the first client's socket path for clients 2 and 3: they
	// dial the same daemon, so everyone sees the same state.
	c2 := cerbapi.NewSocketClient(c1.DialPath())
	c3 := cerbapi.NewSocketClient(c1.DialPath())

	// Add a service.
	if werr := os.WriteFile(path, []byte(`
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
  - id: two
    name: Two
    dir: /tmp/two
    command: ["echo", "two"]
`), 0600); werr != nil {
		t.Fatal(werr)
	}

	// Each long-lived client sees the new service on its next call.
	for i, c := range []*cerbapi.SocketClient{c1, c2, c3} {
		tool := NewCerberusStatusTool(c)
		out, err := tool.Handler(map[string]interface{}{})
		if err != nil {
			t.Fatalf("client %d: %v", i+1, err)
		}
		if !strings.Contains(out, `"id": "two"`) {
			t.Fatalf("client %d: did not see service two after config edit: %s", i+1, out)
		}
	}
}
