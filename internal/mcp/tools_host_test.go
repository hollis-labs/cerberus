package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

const mcpHostsYAML = `version: 1
hosts:
  - {name: box-a, role: worker, labels: {agents: "true"}}
  - {name: box-b, role: worker}
  - {name: box-c, role: service}
  - {name: box-d, role: worker}
`

// hostToolsOverSocket serves the host service on a real daemon socket and
// returns the tools a `cerberus mcp` child would build over it.
func hostToolsOverSocket(t *testing.T) []Tool {
	t.Helper()
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "hosts.yaml")
	if err := os.WriteFile(hostsPath, []byte(mcpHostsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "state.json")
	now := time.Now().UTC()
	store := hosts.NewStore(statePath)
	put := func(name string, age time.Duration, p *hosts.Probed) {
		at := now.Add(-age)
		if err := store.Put(name, hosts.State{State: hosts.StateOnline, CheckedAt: &at, ProbedAt: &at, Probed: p}); err != nil {
			t.Fatal(err)
		}
	}
	const gib = int64(1 << 30)
	// box-a: fresh, 8 GiB free. box-b: fresh, 1 GiB free. box-c: never probed.
	// box-d: measured two hours ago.
	put("box-a", time.Minute, &hosts.Probed{Source: "posix", CPU: 8, MemoryAvailable: 8 * gib, DiskTotal: 200 * gib, DiskFree: 100 * gib, Tools: map[string]bool{"docker": true}})
	put("box-b", time.Minute, &hosts.Probed{Source: "posix", CPU: 2, MemoryAvailable: gib, DiskTotal: 20 * gib, DiskFree: 10 * gib, Tools: map[string]bool{"docker": true}})
	put("box-d", 2*time.Hour, &hosts.Probed{Source: "posix", CPU: 16, MemoryAvailable: 32 * gib, DiskTotal: 200 * gib, DiskFree: 100 * gib})
	svc := cerbapi.NewHostService(audit.NewMemory(), cerbapi.HostServiceOptions{HostsPath: hostsPath, StatePath: statePath})

	sockPath := shortSocketPath(t)
	srv := cerbapi.NewSocketServer(cerbapi.NewInProcessClient(cerbapi.WithHostService(svc), cerbapi.WithInProcessAudit(audit.NewMemory())), sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("socket server: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if conn, err := net.Dial("unix", sockPath); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
	}
	return HostTools(cerbapi.NewSocketClient(sockPath))
}

func callHostTool(t *testing.T, tools []Tool, name string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	cs := connectTools(t, tools...)
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// textBlocks is everything a model reads: the content blocks, never _meta.
func textBlocks(res *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(text.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// What an agent must act on is in the content text: the hosts that could not
// be judged and why, the exclusions and why, and that nothing was ranked.
func TestHostCapacityToolPutsWhatMattersInTheText(t *testing.T) {
	tools := hostToolsOverSocket(t)
	res := callHostTool(t, tools, "cerberus_host_capacity", map[string]any{"min_memory_available": "4GiB", "explain": true})
	if res.IsError {
		t.Fatalf("refused: %s", textBlocks(res))
	}
	text := textBlocks(res)
	for _, want := range []string{
		"UNKNOWN box-c (not guessed to fit): the host has no probe data",
		"run `cerberus host probe box-c`",
		"UNKNOWN box-d (not guessed to fit): the probe data is",
		"beyond the 5m0s limit",
		"EXCLUDED box-b: memory_available is 1073741824 bytes, below the 4294967296 bytes required",
		"1 matched, 2 unknown, of 4 declared hosts",
		"not ranked, and none is reserved",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the content text lacks %q:\n%s", want, text)
		}
	}
	// The first block is the result's own JSON, so a client can read it.
	var body struct {
		Matched []struct {
			Host struct{ Name string } `json:"host"`
		} `json:"matched"`
		Unknown []struct {
			Name    string   `json:"name"`
			Reasons []string `json:"reasons"`
		} `json:"unknown"`
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(firstText(res)), &body); err != nil {
		t.Fatalf("the first block is not JSON: %v\n%s", err, firstText(res))
	}
	if len(body.Matched) != 1 || body.Matched[0].Host.Name != "box-a" || len(body.Unknown) != 2 || len(body.Notes) == 0 {
		t.Fatalf("body = %+v", body)
	}
	// None of it is carried only in _meta.
	for key := range res.Meta {
		if strings.Contains(strings.ToLower(key), "reason") || strings.Contains(strings.ToLower(key), "unknown") {
			t.Errorf("_meta carries %q", key)
		}
	}

	// Without explain, the exclusions stay out and the unknown hosts do not.
	res = callHostTool(t, tools, "cerberus_host_capacity", map[string]any{"min_memory_available": "4GiB"})
	text = textBlocks(res)
	if strings.Contains(text, "EXCLUDED") || !strings.Contains(text, "UNKNOWN box-c") {
		t.Errorf("without explain:\n%s", text)
	}
}

func TestHostListAndGetCallOutStaleAndUnprobedHosts(t *testing.T) {
	tools := hostToolsOverSocket(t)
	res := callHostTool(t, tools, "cerberus_host_list", map[string]any{"role": "worker"})
	text := textBlocks(res)
	for _, want := range []string{"box-b", "box-d: the facts are stale", "box-a"} {
		if !strings.Contains(text, want) {
			t.Errorf("list text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "box-c") {
		t.Errorf("the role filter let box-c through:\n%s", text)
	}
	res = callHostTool(t, tools, "cerberus_host_get", map[string]any{"name": "box-c"})
	if text := textBlocks(res); !strings.Contains(text, "never probed, so its facts are unknown") {
		t.Errorf("get text:\n%s", text)
	}
	res = callHostTool(t, tools, "cerberus_host_get", map[string]any{"name": "missing"})
	if !res.IsError || !strings.Contains(textBlocks(res), "is not declared; see `cerberus host list`") {
		t.Errorf("missing host: error=%v %s", res.IsError, textBlocks(res))
	}
}

// A refusal's guidance reaches the model intact, as an error.
func TestHostCapacityToolRefusalIsGuidance(t *testing.T) {
	tools := hostToolsOverSocket(t)
	res := callHostTool(t, tools, "cerberus_host_capacity", map[string]any{"require_tools": []string{"kubectl"}})
	if !res.IsError {
		t.Fatalf("an unknown tool was accepted: %s", textBlocks(res))
	}
	if text := textBlocks(res); !strings.Contains(text, "docker, tmux, tailscale, caddy") {
		t.Errorf("guidance lost: %s", text)
	}
}

// The four tools are served, bound to read-only contracts, and read-only in
// their annotations.
func TestHostToolsAreReadOnly(t *testing.T) {
	tools := HostTools(cerbapi.HostClientOf(nil))
	if len(tools) != 4 {
		t.Fatalf("%d host tools", len(tools))
	}
	cs := connectTools(t, tools...)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range list.Tools {
		seen[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not annotated read-only", tool.Name)
		}
		op, ok := ToolOperation(tool.Name)
		if !ok || !op.Effect.ReadOnly() {
			t.Errorf("%s is bound to %+v", tool.Name, op)
		}
	}
	for _, name := range []string{"cerberus_host_list", "cerberus_host_get", "cerberus_host_probe", "cerberus_host_capacity"} {
		if !seen[name] {
			t.Errorf("%s not served", name)
		}
	}
}

// The component tools are served, read-only and bound to read contracts.
func TestComponentToolsAreReadOnly(t *testing.T) {
	tools := ComponentTools(cerbapi.ComponentClientOf(nil))
	if len(tools) != 2 {
		t.Fatalf("%d component tools", len(tools))
	}
	cs := connectTools(t, tools...)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range list.Tools {
		seen[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not annotated read-only", tool.Name)
		}
		if op, ok := ToolOperation(tool.Name); !ok || !op.Effect.ReadOnly() {
			t.Errorf("%s is bound to %+v", tool.Name, op)
		}
	}
	for _, name := range []string{"cerberus_component_list", "cerberus_component_get"} {
		if !seen[name] {
			t.Errorf("%s not served", name)
		}
	}
	// Without host support the tool says so rather than returning nothing.
	res := callHostTool(t, tools, "cerberus_component_get", map[string]any{"id": "x"})
	if !res.IsError || !strings.Contains(textBlocks(res), "host support is not available") {
		t.Fatalf("unavailable = %v %s", res.IsError, textBlocks(res))
	}
}

// The write tools carry a revision, change hosts.yaml only when the whole
// result validates, and are read-write in their annotations.
func TestHostWriteToolsOverTheSocket(t *testing.T) {
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "hosts.yaml")
	body := "version: 1\nhosts:\n  - {name: box-a, role: worker}\n"
	if err := os.WriteFile(hostsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := cerbapi.NewHostService(audit.NewMemory(), cerbapi.HostServiceOptions{HostsPath: hostsPath, StatePath: "-"})
	sockPath := shortSocketPath(t)
	srv := cerbapi.NewSocketServer(cerbapi.NewInProcessClient(cerbapi.WithHostService(svc), cerbapi.WithInProcessAudit(audit.NewMemory())), sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if conn, err := net.Dial("unix", sockPath); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
	}
	tools := HostWriteTools(cerbapi.NewSocketClient(sockPath))
	for _, tool := range tools {
		op, ok := ToolOperation(tool.Name)
		if !ok {
			t.Errorf("%s has no contract binding", tool.Name)
		}
		if strings.HasSuffix(tool.Name, "revision") || strings.HasSuffix(tool.Name, "history") {
			continue
		}
		if op.Effect.ReadOnly() {
			t.Errorf("%s is bound to a read", tool.Name)
		}
	}
	var rev struct{ Revision string }
	if err := json.Unmarshal([]byte(textBlocks(callHostTool(t, tools, "cerberus_host_revision", nil))), &rev); err != nil || rev.Revision == "" {
		t.Fatalf("revision: %v", err)
	}
	res := callHostTool(t, tools, "cerberus_host_update", map[string]any{"name": "box-a", "revision": rev.Revision, "acknowledged": true,
		"patch": map[string]any{"labels": map[string]any{"zone": "a"}}})
	if res.IsError || !strings.Contains(textBlocks(res), `"labels.zone"`) {
		t.Fatalf("update: %v %s", res.IsError, textBlocks(res))
	}
	// The same, now-stale revision is refused in words an agent can act on.
	res = callHostTool(t, tools, "cerberus_host_update", map[string]any{"name": "box-a", "revision": rev.Revision, "acknowledged": true,
		"patch": map[string]any{"role": "service"}})
	if !res.IsError || !strings.Contains(textBlocks(res), "changed since you read it") {
		t.Fatalf("stale: %v %s", res.IsError, textBlocks(res))
	}
	res = callHostTool(t, tools, "cerberus_host_history", map[string]any{"name": "box-a"})
	if res.IsError || !strings.Contains(textBlocks(res), "host_update") {
		t.Fatalf("history: %s", textBlocks(res))
	}
}
