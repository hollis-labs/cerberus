package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/cerbapi"
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
