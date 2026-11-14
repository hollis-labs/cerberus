package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/standby"
)

// An agent's MCP call that standby refuses reaches the agent as the daemon's
// guidance, whole: it names the new CLI and how to leave standby. The call
// travels the real lane: tool, socket client, socket, fence.
func TestStandbyRefusalReachesAnMCPCallerIntact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgPath := filepath.Join(home, "config.yaml")
	cfg := "version: 2\nresources:\n  - id: other\n    type: process\n    connector: local\n    env: dev\n    owner: self\n    config:\n      mode: dev_session\n      command: [\"/bin/sleep\", \"60\"]\n      dir: " + home + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := cerbapi.NewResourceRuntimeService(audit.NewMemory(), cerbapi.WithResourceRuntimeConfigPath(cfgPath))
	fence := cerbapi.NewStandby(runtime, filepath.Join(home, "standby.d", "audit"), nil)
	fence.Set(standby.State{Active: true, Resource: "cerberus-substrate"})

	sockPath := filepath.Join("/tmp", fmt.Sprintf("cs-mcp-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	srv := cerbapi.NewSocketServer(cerbapi.NewInProcessClient(cerbapi.WithResourceRuntimeService(runtime)), sockPath, cerbapi.WithStandby(fence))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("socket server: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(3 * time.Second); ; {
		if conn, err := net.Dial("unix", sockPath); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}

	tool := withRequestScope(NewCerberusResourceStopTool(cerbapi.NewSocketClient(sockPath)))
	_, err := tool.Handler(context.Background(), map[string]any{"resource_id": "other", "acknowledged": true})
	if err == nil {
		t.Fatal("standby let an MCP stop through")
	}
	// Uncoded, so the tool returns it as an error whose text is the agent's
	// message; it must be the daemon's guidance whole, not a redacted remnant.
	got := err.Error()
	for _, want := range []string{"is in standby", "new `cerberus` CLI", "remove ~/.cerberus/standby", `"cerberus-substrate"`, "POST /resources/other/stop"} {
		if !strings.Contains(got, want) {
			t.Errorf("tool error = %q, want it to keep %q", got, want)
		}
	}
}
