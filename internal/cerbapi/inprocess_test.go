package cerbapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/service"
)

// writeCfg seeds ~/.cerberus-style config into a temp dir and returns the
// full path. Services listed here are never actually started — they're
// just used as registry entries so the DTO pipeline can run end-to-end.
func writeCfg(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestRegistry(t *testing.T, content string) (*service.ServiceRegistry, string) {
	t.Helper()
	dir := t.TempDir()
	path := writeCfg(t, dir, content)
	reg, err := service.NewServiceRegistry(config.NewFileSource(path), nil)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return reg, path
}

func TestInProcessClient_ListServices(t *testing.T) {
	reg, _ := newTestRegistry(t, `
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
`)
	c := NewInProcessClient(reg)
	out, err := c.ListServices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 services, got %d", len(out))
	}
	ids := []string{out[0].ID, out[1].ID}
	if ids[0] != "one" || ids[1] != "two" {
		t.Fatalf("unexpected service order: %v", ids)
	}
}

func TestInProcessClient_GetService_NotFound(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	c := NewInProcessClient(reg)
	if _, err := c.GetService(context.Background(), "missing"); err == nil {
		t.Fatal("expected error for missing service")
	}
}

// TestInProcessClient_StartService_NotFound is the Start-side counterpart
// to the "unknown service" error path used by every lifecycle op. We
// intentionally do NOT exercise the happy path because that would spawn
// a real process — covered by cerberus CLI integration tests elsewhere.
func TestInProcessClient_StartService_NotFound(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	c := NewInProcessClient(reg)
	res, err := c.StartService(context.Background(), "missing")
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.Success {
		t.Fatal("expected Success=false for missing service")
	}
	if !strings.Contains(res.Error, "not found") {
		t.Fatalf("expected 'not found' in error, got: %s", res.Error)
	}
}

// TestInProcessClient_StopService_Protected ensures the protected-gate
// from the MCP tool contract is preserved at the Client layer.
func TestInProcessClient_StopService_Protected(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services:
  - id: proto
    name: Protected
    dir: /tmp/proto
    command: ["echo", "x"]
    protected: true
`)
	c := NewInProcessClient(reg)
	res, err := c.StopService(context.Background(), "proto", AuditContext{Reason: "test"})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.Success {
		t.Fatal("protected service must not be stoppable")
	}
	if !strings.Contains(res.Error, "protected") {
		t.Fatalf("expected protected-error, got: %s", res.Error)
	}
}

// TestInProcessClient_StopService_ReloadsConfig is the core CERB-2 repro
// at the Client layer. Baseline has no service "late"; we add it to
// disk; the next StopService call must see it (and reject the stop
// because there's no process, not because the service doesn't exist).
//
// Importantly, we do NOT reconstruct the Client — the whole point of
// CERB-2 is that a long-lived Client handle picks up config edits
// without restart.
func TestInProcessClient_StopService_ReloadsConfig(t *testing.T) {
	reg, path := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	c := NewInProcessClient(reg)

	// Baseline: "late" is not registered.
	res, err := c.StopService(context.Background(), "late", AuditContext{Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success || !strings.Contains(res.Error, "not found") {
		t.Fatalf("baseline expected not-found; got: %+v", res)
	}

	// Add "late" to config on disk.
	if werr := os.WriteFile(path, []byte(`
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
  - id: late
    name: Late
    dir: /tmp/late
    command: ["echo", "late"]
`), 0600); werr != nil {
		t.Fatal(werr)
	}

	// Post-edit: client reloads registry internally; "late" resolves.
	// The op then returns success=true because Stop() is a no-op when
	// no process is actually running for the service.
	res, err = c.StopService(context.Background(), "late", AuditContext{Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("post-edit: expected client to see newly-added service; got error: %s", res.Error)
	}
}

func TestInProcessClient_Health_UnknownService(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	c := NewInProcessClient(reg)
	if _, err := c.Health(context.Background(), "missing"); err == nil {
		t.Fatal("expected error for unknown service")
	}
}

func TestInProcessClient_Health_ListAll(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	c := NewInProcessClient(reg)
	h, err := c.Health(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Services) != 1 {
		t.Fatalf("want 1 health entry, got %d", len(h.Services))
	}
}

func TestInProcessClient_BuildService_NoBuildCmd(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	c := NewInProcessClient(reg)
	res, err := c.BuildService(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Fatal("service with no build command must return success=false")
	}
}

func TestInProcessClient_ListProjectsResourcesPipelines(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services: []
`)
	cfg := &config.ConfigV2{
		Version: 2,
		Projects: []config.ProjectDef{
			{ID: "p1", Name: "Project One"},
		},
		Resources: []config.ResourceDef{
			{ID: "r1", Name: "Res One", Project: "p1", Connector: "local", Tags: []string{"dev"}},
		},
		Pipelines: []config.PipelineDef{
			{ID: "pipe1", Name: "Pipe One", Stages: []config.StageDef{{Name: "s1"}}},
		},
	}
	c := NewInProcessClient(reg, WithConfigV2(cfg))

	projects, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Resources != 1 {
		t.Fatalf("unexpected projects: %+v", projects)
	}

	resources, err := c.ListResources(context.Background(), ResourceListArgs{Tag: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 {
		t.Fatalf("want 1 resource with tag=dev, got %d", len(resources))
	}

	resources, err = c.ListResources(context.Background(), ResourceListArgs{Tag: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 0 {
		t.Fatalf("want 0 resources with tag=nope, got %d", len(resources))
	}

	pipelines, err := c.ListPipelines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pipelines) != 1 || pipelines[0].Stages != 1 {
		t.Fatalf("unexpected pipelines: %+v", pipelines)
	}
}
