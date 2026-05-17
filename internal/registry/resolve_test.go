package registry

import (
	"os"
	"path/filepath"
	"testing"
)

const globalConfigYAML = `version: 2
projects:
  - id: clockwork
    name: Clockwork (global)
resources:
  - id: clockwork-api
    name: GLOBAL
    type: process
    connector: local
    project: clockwork
  - id: legacy-only
    name: Legacy Only
    type: process
    connector: local
    project: clockwork
`

func TestResolveRegisteredOnly(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, DefaultIndexFilename)
	reg := New(indexPath)
	cfgDir := t.TempDir()
	mustRegister(t, reg, writeProjectConfig(t, cfgDir, "clockwork"))
	mustRegister(t, reg, writeProjectConfig(t, cfgDir, "torque"))

	resolved, err := Resolve(ResolveOptions{IndexPath: indexPath})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved.Config.Resources) != 2 {
		t.Fatalf("resources = %d, want 2", len(resolved.Config.Resources))
	}
	if len(resolved.Config.Projects) != 2 {
		t.Fatalf("projects = %d, want 2", len(resolved.Config.Projects))
	}
}

func TestResolveGlobalOnly(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(globalPath, []byte(globalConfigYAML), 0o600); err != nil {
		t.Fatalf("write global: %v", err)
	}
	resolved, err := Resolve(ResolveOptions{
		IndexPath:  filepath.Join(dir, DefaultIndexFilename), // absent
		GlobalPath: globalPath,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved.Config.Resources) != 2 {
		t.Fatalf("resources = %d, want 2 (global)", len(resolved.Config.Resources))
	}
}

func TestResolveRegisteredWinsOverGlobal(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(globalPath, []byte(globalConfigYAML), 0o600); err != nil {
		t.Fatalf("write global: %v", err)
	}
	indexPath := filepath.Join(dir, DefaultIndexFilename)
	reg := New(indexPath)
	mustRegister(t, reg, writeProjectConfig(t, t.TempDir(), "clockwork"))

	resolved, err := Resolve(ResolveOptions{IndexPath: indexPath, GlobalPath: globalPath})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// clockwork-api exists in both — registered config must win.
	var apiName string
	for _, r := range resolved.Config.Resources {
		if r.ID == "clockwork-api" {
			apiName = r.Name
		}
	}
	if apiName == "GLOBAL" {
		t.Error("clockwork-api still has the global definition; registered config must win")
	}
	if apiName != "clockwork API" {
		t.Errorf("clockwork-api name = %q, want registered value", apiName)
	}
	// legacy-only exists only in the global file — it must survive.
	found := false
	for _, r := range resolved.Config.Resources {
		if r.ID == "legacy-only" {
			found = true
		}
	}
	if !found {
		t.Error("legacy-only dropped; global-only ids must survive the merge")
	}
}

func TestResolveSkipsBrokenRegisteredConfig(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, DefaultIndexFilename)
	reg := New(indexPath)
	cfgDir := t.TempDir()
	mustRegister(t, reg, writeProjectConfig(t, cfgDir, "clockwork"))
	brokenPath := writeProjectConfig(t, cfgDir, "torque")
	mustRegister(t, reg, brokenPath)

	if err := os.Remove(brokenPath); err != nil {
		t.Fatalf("remove: %v", err)
	}

	resolved, err := Resolve(ResolveOptions{IndexPath: indexPath})
	if err != nil {
		t.Fatalf("Resolve must not fail on one broken config: %v", err)
	}
	if len(resolved.Config.Resources) != 1 {
		t.Errorf("resources = %d, want 1 (broken one skipped)", len(resolved.Config.Resources))
	}
	if len(resolved.Skipped) != 1 || resolved.Skipped[0].Owner != "torque" {
		t.Errorf("Skipped = %+v, want one torque entry", resolved.Skipped)
	}
	if len(resolved.Warnings) == 0 {
		t.Error("expected a warning for the skipped config")
	}
}

func TestResolveConfigDerivesSiblingIndex(t *testing.T) {
	// config.yaml and registry.yaml live in the same dir; ResolveConfig
	// must find the sibling index without being told its path.
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(globalPath, []byte("version: 2\nprojects: []\nresources: []\n"), 0o600); err != nil {
		t.Fatalf("write global: %v", err)
	}
	reg := New(filepath.Join(dir, DefaultIndexFilename))
	mustRegister(t, reg, writeProjectConfig(t, t.TempDir(), "clockwork"))

	cfg, err := ResolveConfig(globalPath)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if len(cfg.Resources) != 1 || cfg.Resources[0].ID != "clockwork-api" {
		t.Fatalf("resources = %+v, want clockwork-api from sibling index", cfg.Resources)
	}
}

func mustRegister(t *testing.T, reg *Registry, path string) {
	t.Helper()
	if _, err := reg.Register(path); err != nil {
		t.Fatalf("Register %s: %v", path, err)
	}
}
