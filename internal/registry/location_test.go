package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterRefusesCentralLocations(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	stateDir := filepath.Join(homeDir, ".cerberus")
	central := filepath.Join(stateDir, "projects")
	if err := os.MkdirAll(central, 0700); err != nil {
		t.Fatal(err)
	}
	centralPath := writeProjectConfig(t, central, "central")
	repoDir := t.TempDir()
	repoPath := writeProjectConfig(t, repoDir, "owned")
	alias := filepath.Join(repoDir, "alias.cerberus.yaml")
	if err := os.Symlink(centralPath, alias); err != nil {
		t.Fatal(err)
	}
	centralAlias := filepath.Join(central, "owned.cerberus.yaml")
	if err := os.Symlink(repoPath, centralAlias); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(repoDir, DefaultBundleFilename)
	if err := os.WriteFile(manifest, []byte("kind: cerberus-bundle/v1\nprojects: ["+centralPath+"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reg := New(filepath.Join(stateDir, DefaultIndexFilename))
	for _, path := range []string{centralPath, alias, centralAlias, manifest} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := reg.Register(path); err == nil || !strings.Contains(err.Error(), "owning repo") {
				t.Fatalf("Register(%s) = %v", path, err)
			}
		})
	}
	// Refusing a bundle must not persist any part of the attempted registration.
	if _, err := os.Stat(reg.IndexPath()); !os.IsNotExist(err) {
		t.Fatalf("rejected registration wrote an index: %v", err)
	}
	if _, err := reg.Register(repoPath); err != nil {
		t.Fatalf("repo-owned config rejected: %v", err)
	}
	// An unrelated folder named projects is not Cerberus's central state lane.
	projectsDir := filepath.Join(repoDir, "projects")
	if err := os.Mkdir(projectsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Register(writeProjectConfig(t, projectsDir, "child")); err != nil {
		t.Fatal(err)
	}
}

func TestCustomStateCentralEntryIsInvalidAndSkipped(t *testing.T) {
	stateDir := t.TempDir()
	central := filepath.Join(stateDir, "projects")
	if err := os.Mkdir(central, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeProjectConfig(t, central, "legacy")
	indexPath := filepath.Join(stateDir, DefaultIndexFilename)
	reg := New(indexPath)
	if _, err := reg.Register(path); err == nil {
		t.Fatal("custom central location accepted")
	}
	// Simulate an index written by a previous Cerberus version.
	index := &Index{Entries: []IndexEntry{{Owner: "legacy", Path: path, Kind: ProjectConfigKind}}}
	if err := index.Save(indexPath); err != nil {
		t.Fatal(err)
	}
	reports, err := reg.Health()
	if err != nil {
		t.Fatal(err)
	}
	if reports[0].Status != HealthInvalid || !strings.Contains(reports[0].Detail, "retired centralized directory") {
		t.Fatalf("health = %#v", reports)
	}
	resolved, err := Resolve(ResolveOptions{IndexPath: indexPath})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Skipped[0].Owner != "legacy" || resolved.Skipped[0].Status != HealthInvalid {
		t.Fatalf("skip diagnostics = %#v", resolved.Skipped)
	}
	for _, resource := range resolved.Config.Resources {
		if resource.ID == "legacy-api" {
			t.Fatal("central resource remained in runtime resolution")
		}
	}
}
