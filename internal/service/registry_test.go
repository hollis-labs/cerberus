package service

import (
	"errors"
	"testing"

	"github.com/chrispian/cerberus/internal/config"
)

func newSvcDef(id string, cmd ...string) config.ServiceDef {
	return config.ServiceDef{
		ID:      id,
		Name:    id,
		Dir:     "/tmp/" + id,
		Command: cmd,
	}
}

func TestRegistryInitialLoad(t *testing.T) {
	cfg := &config.Config{Version: 1, Services: []config.ServiceDef{
		newSvcDef("a", "run"),
		newSvcDef("b", "run"),
	}}
	src := config.NewStaticSource(cfg)
	reg, err := NewServiceRegistry(src, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cur := reg.Current()
	if len(cur) != 2 {
		t.Fatalf("want 2 services, got %d", len(cur))
	}
	if reg.Find("a") == nil || reg.Find("b") == nil {
		t.Fatal("expected find to resolve ids a and b")
	}
	if reg.Find("missing") != nil {
		t.Fatal("expected nil for missing id")
	}
}

func TestRegistryReloadDetectsAdded(t *testing.T) {
	src := config.NewStaticSource(&config.Config{Services: []config.ServiceDef{newSvcDef("a", "run")}})
	reg, err := NewServiceRegistry(src, nil)
	if err != nil {
		t.Fatal(err)
	}

	src.Set(&config.Config{Services: []config.ServiceDef{
		newSvcDef("a", "run"),
		newSvcDef("b", "run"),
	}})
	if err := reg.Reload(); err != nil {
		t.Fatal(err)
	}

	if reg.Find("b") == nil {
		t.Fatal("newly added service b not found after reload")
	}
	if len(reg.Current()) != 2 {
		t.Fatalf("want 2 services, got %d", len(reg.Current()))
	}
}

func TestRegistryReloadPreservesPointerIdentity(t *testing.T) {
	src := config.NewStaticSource(&config.Config{Services: []config.ServiceDef{newSvcDef("a", "run")}})
	reg, err := NewServiceRegistry(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := reg.Find("a")
	if before == nil {
		t.Fatal("service a missing")
	}

	// Same config — reload must reuse the same *ManagedService pointer
	// so the MCP tools' captured pointers keep working.
	src.Set(&config.Config{Services: []config.ServiceDef{newSvcDef("a", "run")}})
	if err := reg.Reload(); err != nil {
		t.Fatal(err)
	}
	after := reg.Find("a")
	if before != after {
		t.Fatal("pointer identity lost across no-op reload")
	}
}

func TestRegistryReloadMarksChangedStale(t *testing.T) {
	src := config.NewStaticSource(&config.Config{Services: []config.ServiceDef{newSvcDef("a", "run")}})
	reg, err := NewServiceRegistry(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := reg.Find("a")
	if svc.Stale {
		t.Fatal("expected Stale=false on initial load")
	}

	// Change the command — the 2026-04-18 incident case.
	src.Set(&config.Config{Services: []config.ServiceDef{newSvcDef("a", "run", "-v")}})
	if err := reg.Reload(); err != nil {
		t.Fatal(err)
	}

	if !svc.Stale {
		t.Fatal("expected Stale=true after command change")
	}
	if len(svc.Def.Command) != 2 || svc.Def.Command[1] != "-v" {
		t.Fatalf("expected Def updated in-place, got %v", svc.Def.Command)
	}
}

func TestRegistryReloadHandlesRemoved(t *testing.T) {
	src := config.NewStaticSource(&config.Config{Services: []config.ServiceDef{
		newSvcDef("a", "run"),
		newSvcDef("b", "run"),
	}})
	reg, err := NewServiceRegistry(src, nil)
	if err != nil {
		t.Fatal(err)
	}

	src.Set(&config.Config{Services: []config.ServiceDef{newSvcDef("a", "run")}})
	if err := reg.Reload(); err != nil {
		t.Fatal(err)
	}

	if reg.Find("b") != nil {
		t.Fatal("service b should be unregistered after removal from config")
	}
	if len(reg.Current()) != 1 {
		t.Fatalf("want 1 service, got %d", len(reg.Current()))
	}
}

func TestRegistryReloadInvalidConfigKeepsLastGood(t *testing.T) {
	cfg := &config.Config{Services: []config.ServiceDef{newSvcDef("a", "run")}}
	src := config.NewStaticSource(cfg)
	reg, err := NewServiceRegistry(src, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a parse error on the next reload.
	src.SetError(errors.New("bad yaml"))
	err = reg.Reload()
	if err == nil {
		t.Fatal("expected reload to return an error")
	}

	// The registry MUST NOT drop the last-good snapshot.
	if reg.Find("a") == nil {
		t.Fatal("last-good config was discarded after failed reload")
	}
	if len(reg.Current()) != 1 {
		t.Fatalf("want 1 service preserved, got %d", len(reg.Current()))
	}
}

func TestRegistryRequiresSource(t *testing.T) {
	if _, err := NewServiceRegistry(nil, nil); err == nil {
		t.Fatal("expected error when source is nil")
	}
}

func TestRegistryCurrentIsCopy(t *testing.T) {
	src := config.NewStaticSource(&config.Config{Services: []config.ServiceDef{newSvcDef("a", "run")}})
	reg, err := NewServiceRegistry(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	cur := reg.Current()
	cur[0] = nil
	// Mutating the returned slice must not clobber registry state.
	if reg.Find("a") == nil {
		t.Fatal("Current() returned live internal slice")
	}
}
