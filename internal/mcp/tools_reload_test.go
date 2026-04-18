package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/service"
)

// writeConfig is a helper for integration tests.
func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestStatusToolPicksUpAddedServiceWithoutBounce reproduces the class of
// bug behind the 2026-04-18 incident: the MCP tool reads live from disk,
// so a service added to ~/.cerberus/config.yaml after the daemon started
// appears in the next cerberus_status call — no daemon restart required.
func TestStatusToolPicksUpAddedServiceWithoutBounce(t *testing.T) {
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

	client := cerbapi.NewInProcessClient(reg)
	tool := NewCerberusStatusTool(client)

	// Baseline: one service.
	out, err := tool.Handler(map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id": "one"`) {
		t.Fatalf("baseline status missing service one: %s", out)
	}

	// Edit config — add a second service. No daemon restart.
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
		t.Fatalf("status tool did not pick up newly-added service two: %s", out)
	}
}

// TestRebuildToolUsesFreshCommandAfterEdit is the direct repro of the
// 2026-04-18 incident: operator edits the command in config.yaml, then
// calls cerberus_rebuild, and the MCP tool should observe the new
// command without any daemon bounce.
//
// We can't fully exercise svc.Start() (it tries to exec real binaries),
// but we can verify the Def the tool sees matches the new config.
func TestRebuildToolUsesFreshCommandAfterEdit(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
services:
  - id: stale
    name: Stale
    dir: /tmp/stale
    command: ["go", "build", "./..."]
    build: ["go", "build", "./..."]
`)
	reg, err := service.NewServiceRegistry(config.NewFileSource(path), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Edit the config — same ID, different command.
	if err := os.WriteFile(path, []byte(`
version: 1
services:
  - id: stale
    name: Stale
    dir: /tmp/stale
    command: ["clockwork", "serve"]
    build: ["go", "install", "./..."]
`), 0600); err != nil {
		t.Fatal(err)
	}

	// Trigger a reload via the Client surface — matches what the tool
	// handler would do on a real invocation.
	client := cerbapi.NewInProcessClient(reg)
	if _, gerr := client.GetService(context.Background(), "stale"); gerr != nil {
		t.Fatal(gerr)
	}
	svc := reg.Find("stale")
	if svc == nil {
		t.Fatal("service lost across reload")
	}
	if !strings.Contains(strings.Join(svc.Def.Command, " "), "clockwork serve") {
		t.Fatalf("tool reads stale command after reload: %v", svc.Def.Command)
	}
	if !strings.Contains(strings.Join(svc.Def.Build, " "), "go install") {
		t.Fatalf("tool reads stale build command after reload: %v", svc.Def.Build)
	}
}

// TestPipelineRunToolSeesServiceAddedAfterConstruction verifies the
// cerberus_pipeline_run tool routes through the ServiceRegistry (not a
// captured slice), so services added to ~/.cerberus/config.yaml after the
// daemon started are reachable by pipelines without a daemon bounce.
//
// The tool is constructed with only service `one` in scope. We then add
// service `two` on disk and invoke a pipeline that references `two`. The
// pre-change invocation must fail with "resource not found"; the
// post-change invocation must progress past resolve (the error must not
// contain "not found").
func TestPipelineRunToolSeesServiceAddedAfterConstruction(t *testing.T) {
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

	// ConfigV2 with a pipeline that builds service `two` — which does
	// NOT exist in the registry at tool-construction time.
	cfg := &config.ConfigV2{
		Version: 2,
		Pipelines: []config.PipelineDef{
			{
				ID:   "build-two",
				Name: "Build Two",
				Stages: []config.StageDef{
					{
						Name: "build",
						Actions: []config.ActionDef{
							{Type: "build", Resource: "two"},
						},
					},
				},
			},
		},
	}

	client := cerbapi.NewInProcessClient(reg, cerbapi.WithConfigV2(cfg))
	tool := NewCerberusPipelineRunTool(client)

	// Baseline: service two doesn't exist — resolve must fail with
	// "resource not found".
	out, err := tool.Handler(map[string]interface{}{"pipeline_id": "build-two"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not found") {
		t.Fatalf("baseline: expected resolve error for missing service, got: %s", out)
	}

	// Add service two on disk — no daemon restart, no tool
	// re-construction.
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
    build: ["echo", "building two"]
`), 0600); werr != nil {
		t.Fatal(werr)
	}

	// Invoke the tool again — it must reload the registry internally
	// and resolve the action against the fresh service list. If the
	// tool captured the slice at construction time, this would still
	// fail with "not found".
	out, err = tool.Handler(map[string]interface{}{"pipeline_id": "build-two"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "not found") {
		t.Fatalf("after reload: tool still reports service two as not found, indicates stale slice: %s", out)
	}
}

// TestStatusToolSurfacesStaleFlag ensures the registry's stale marking
// is plumbed through to the JSON response so operators can see when a
// running process no longer matches the on-disk config.
func TestStatusToolSurfacesStaleFlag(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
services:
  - id: svc
    name: Service
    dir: /tmp/svc
    command: ["echo", "v1"]
`)
	reg, err := service.NewServiceRegistry(config.NewFileSource(path), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the service having been "started" so stale makes sense.
	// We can set Stale manually — it's the same end-state the reload
	// code produces.
	reg.Find("svc").Stale = false

	// Change command and reload.
	if werr := os.WriteFile(path, []byte(`
version: 1
services:
  - id: svc
    name: Service
    dir: /tmp/svc
    command: ["echo", "v2"]
`), 0600); werr != nil {
		t.Fatal(werr)
	}
	if rerr := reg.Reload(); rerr != nil {
		t.Fatal(rerr)
	}

	client := cerbapi.NewInProcessClient(reg)
	tool := NewCerberusStatusTool(client)
	out, err := tool.Handler(map[string]interface{}{"service_id": "svc"})
	if err != nil {
		t.Fatal(err)
	}

	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("unmarshal status: %v — output: %s", err, out)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d: %s", len(entries), out)
	}
	if stale, _ := entries[0]["stale"].(bool); !stale {
		t.Fatalf("expected stale=true in status output: %s", out)
	}
}
