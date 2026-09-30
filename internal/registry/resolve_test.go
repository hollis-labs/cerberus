package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
)

const globalConfigYAML = `version: 2
projects:
  - id: demo
    name: Demo (global)
resources:
  - id: demo-api
    name: GLOBAL
    type: process
    connector: local
    project: demo
  - id: legacy-only
    name: Legacy Only
    type: process
    connector: local
    project: demo
`

func TestResolveRegisteredOnly(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, DefaultIndexFilename)
	reg := New(indexPath)
	cfgDir := t.TempDir()
	mustRegister(t, reg, writeProjectConfig(t, cfgDir, "demo"))
	mustRegister(t, reg, writeProjectConfig(t, cfgDir, "foxtrot"))

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
	mustRegister(t, reg, writeProjectConfig(t, t.TempDir(), "demo"))

	resolved, err := Resolve(ResolveOptions{IndexPath: indexPath, GlobalPath: globalPath})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// demo-api exists in both — registered config must win.
	var apiName string
	for _, r := range resolved.Config.Resources {
		if r.ID == "demo-api" {
			apiName = r.Name
		}
	}
	if apiName == "GLOBAL" {
		t.Error("demo-api still has the global definition; registered config must win")
	}
	if apiName != "demo API" {
		t.Errorf("demo-api name = %q, want registered value", apiName)
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
	mustRegister(t, reg, writeProjectConfig(t, cfgDir, "demo"))
	brokenPath := writeProjectConfig(t, cfgDir, "foxtrot")
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
	if len(resolved.Skipped) != 1 || resolved.Skipped[0].Owner != "foxtrot" {
		t.Errorf("Skipped = %+v, want one foxtrot entry", resolved.Skipped)
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
	mustRegister(t, reg, writeProjectConfig(t, t.TempDir(), "demo"))

	cfg, err := ResolveConfig(globalPath)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if len(cfg.Resources) != 1 || cfg.Resources[0].ID != "demo-api" {
		t.Fatalf("resources = %+v, want demo-api from sibling index", cfg.Resources)
	}
}

func TestResolveRegisteredOutOfRepoConfigWithRegistryURN(t *testing.T) {
	// A shared directory's bootstrap/write-back flow operates against app-owned project
	// configs that may live outside ~/.cerberus and may add shared-directory
	// identity metadata (`registry_urn`). Cerberus must keep resolving the
	// local runtime config from the pointed-to file without copying bodies
	// into the index or rejecting the metadata field.
	stateDir := t.TempDir()
	globalPath := filepath.Join(stateDir, "config.yaml")
	if err := os.WriteFile(globalPath, []byte("version: 2\nprojects: []\nresources: []\n"), 0o600); err != nil {
		t.Fatalf("write global: %v", err)
	}

	repoDir := t.TempDir()
	projectPath := writeProjectConfig(t, repoDir, "demo")
	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	withURN := strings.Replace(string(data), "project:\n", "registry_urn: msg://project/directory/prj_demo\nproject:\n", 1)
	if err := os.WriteFile(projectPath, []byte(withURN), 0o600); err != nil {
		t.Fatalf("rewrite project config with registry_urn: %v", err)
	}

	reg, err := ForConfig(globalPath)
	if err != nil {
		t.Fatalf("ForConfig: %v", err)
	}
	mustRegister(t, reg, projectPath)

	entries, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Path != projectPath {
		t.Fatalf("entry path = %q, want %q", entries[0].Path, projectPath)
	}

	pc, err := LoadProjectConfig(projectPath)
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}
	if pc.RegistryURN != "msg://project/directory/prj_demo" {
		t.Fatalf("registry_urn = %q, want shared URN", pc.RegistryURN)
	}

	cfg, err := ResolveConfig(globalPath)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].ID != "demo" {
		t.Fatalf("projects = %+v, want demo from out-of-repo config", cfg.Projects)
	}
	if len(cfg.Resources) != 1 || cfg.Resources[0].ID != "demo-api" {
		t.Fatalf("resources = %+v, want demo-api from out-of-repo config", cfg.Resources)
	}
}

func mustRegister(t *testing.T, reg *Registry, path string) {
	t.Helper()
	if _, err := reg.Register(path); err != nil {
		t.Fatalf("Register %s: %v", path, err)
	}
}

// The transfer root is the operator's: the global config sets it, and a
// registered project config cannot (B3).
func TestTheTransferRootComesFromTheGlobalConfigOnly(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, DefaultIndexFilename)
	reg := New(indexPath)
	project := writeProjectConfig(t, t.TempDir(), "demo")
	data, readErr := os.ReadFile(project) //nolint:gosec // the test's own temp file
	if readErr != nil {
		t.Fatal(readErr)
	}
	if writeErr := os.WriteFile(project, append(data, []byte("transfers:\n  root: /srv/project-says\n")...), 0o600); writeErr != nil { //nolint:gosec // the test's own temp file
		t.Fatal(writeErr)
	}
	// A project config naming one is refused at registration.
	if _, regErr := reg.Register(project); regErr == nil || !strings.Contains(regErr.Error(), "transfers") {
		t.Fatalf("a project config naming a transfer root registered: %v", regErr)
	}
	resolved, err := Resolve(ResolveOptions{IndexPath: indexPath})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Config.TransferRoot(); got != config.ExpandHomePath(config.DefaultTransferRoot) {
		t.Fatalf("default transfer root = %q", got)
	}

	global := filepath.Join(dir, "config.yaml")
	if writeErr := os.WriteFile(global, []byte("version: 2\ntransfers:\n  root: /srv/operator-says\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	resolved, err = Resolve(ResolveOptions{IndexPath: indexPath, GlobalPath: global})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Config.TransferRoot(); got != "/srv/operator-says" {
		t.Fatalf("transfer root = %q", got)
	}
}
