package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// projectConfigYAML renders a minimal valid project config for owner.
func projectConfigYAML(owner string) string {
	return fmt.Sprintf(`kind: cerberus-project/v1
owner: %[1]s
project:
  id: %[1]s
  name: %[1]s
resources:
  - id: %[1]s-api
    name: %[1]s API
    type: process
    connector: local
    project: %[1]s
`, owner)
}

// writeProjectConfig writes a project config for owner into dir and
// returns its path.
func writeProjectConfig(t *testing.T, dir, owner string) string {
	t.Helper()
	path := filepath.Join(dir, owner+FileSuffix)
	if err := os.WriteFile(path, []byte(projectConfigYAML(owner)), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	return path
}

// newRegistry returns a Registry backed by a fresh temp index.
func newRegistry(t *testing.T) *Registry {
	t.Helper()
	return New(filepath.Join(t.TempDir(), DefaultIndexFilename))
}

func TestRegisterProjectConfig(t *testing.T) {
	reg := newRegistry(t)
	path := writeProjectConfig(t, t.TempDir(), "clockwork")

	entries, err := reg.Register(path)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.Owner != "clockwork" || got.Kind != ProjectConfigKind {
		t.Errorf("entry = %+v, want owner=clockwork kind=%s", got, ProjectConfigKind)
	}
	if !filepath.IsAbs(got.Path) {
		t.Errorf("entry path %q is not absolute", got.Path)
	}
	if got.RegisteredAt == "" {
		t.Error("registered_at is empty")
	}
}

func TestRegisterPersistsAcrossInstances(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), DefaultIndexFilename)
	path := writeProjectConfig(t, t.TempDir(), "torque")

	if _, err := New(indexPath).Register(path); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// A fresh Registry must read the same entry back from disk.
	entries, err := New(indexPath).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Owner != "torque" {
		t.Fatalf("List = %+v, want one torque entry", entries)
	}
}

func TestRegisterOwnerIsIdempotent(t *testing.T) {
	reg := newRegistry(t)
	dir := t.TempDir()
	path := writeProjectConfig(t, dir, "clockwork")

	if _, err := reg.Register(path); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if _, err := reg.Register(path); err != nil {
		t.Fatalf("second Register: %v", err)
	}
	entries, _ := reg.List()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (re-register must replace)", len(entries))
	}
}

func TestRegisterBundleManifest(t *testing.T) {
	reg := newRegistry(t)
	dir := t.TempDir()
	writeProjectConfig(t, dir, "clockwork")
	writeProjectConfig(t, dir, "torque")

	manifest := filepath.Join(dir, DefaultBundleFilename)
	content := "kind: cerberus-bundle/v1\nprojects:\n  - ./clockwork.cerberus.yaml\n  - ./torque.cerberus.yaml\n"
	if err := os.WriteFile(manifest, []byte(content), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	entries, err := reg.Register(manifest)
	if err != nil {
		t.Fatalf("Register manifest: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if e.Via != manifest {
			t.Errorf("entry %s via = %q, want manifest path %q", e.Owner, e.Via, manifest)
		}
	}
}

func TestRegisterInvalidConfigIsAtomic(t *testing.T) {
	reg := newRegistry(t)
	dir := t.TempDir()
	good := writeProjectConfig(t, dir, "clockwork")
	if _, err := reg.Register(good); err != nil {
		t.Fatalf("register good: %v", err)
	}

	// A config with port: 0 must be rejected...
	bad := filepath.Join(dir, "bad.cerberus.yaml")
	badYAML := projectConfigYAML("badapp") + "    config:\n      port: 0\n"
	if err := os.WriteFile(bad, []byte(badYAML), 0o600); err != nil {
		t.Fatalf("write bad config: %v", err)
	}
	if _, err := reg.Register(bad); err == nil {
		t.Fatal("expected Register to reject a port:0 config")
	}
	// ...and the prior index must be untouched.
	entries, _ := reg.List()
	if len(entries) != 1 || entries[0].Owner != "clockwork" {
		t.Fatalf("index = %+v, want only clockwork after failed register", entries)
	}
}

func TestRegisterDirectoryFindsManifest(t *testing.T) {
	reg := newRegistry(t)
	dir := t.TempDir()
	writeProjectConfig(t, dir, "clockwork")
	manifest := filepath.Join(dir, DefaultBundleFilename)
	if err := os.WriteFile(manifest, []byte("kind: cerberus-bundle/v1\nprojects: [./clockwork.cerberus.yaml]\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	entries, err := reg.Register(dir)
	if err != nil {
		t.Fatalf("Register dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
}

func TestDeregister(t *testing.T) {
	reg := newRegistry(t)
	path := writeProjectConfig(t, t.TempDir(), "clockwork")
	if _, err := reg.Register(path); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Deregister("clockwork"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if entries, _ := reg.List(); len(entries) != 0 {
		t.Fatalf("entries = %d, want 0 after deregister", len(entries))
	}
	if err := reg.Deregister("clockwork"); err == nil {
		t.Fatal("deregister of unknown owner must error")
	}
}

func TestListIsSorted(t *testing.T) {
	reg := newRegistry(t)
	dir := t.TempDir()
	for _, owner := range []string{"torque", "clockwork", "sigil"} {
		if _, err := reg.Register(writeProjectConfig(t, dir, owner)); err != nil {
			t.Fatalf("Register %s: %v", owner, err)
		}
	}
	entries, _ := reg.List()
	got := []string{entries[0].Owner, entries[1].Owner, entries[2].Owner}
	want := []string{"clockwork", "sigil", "torque"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List order = %v, want %v", got, want)
		}
	}
}

func TestHealth(t *testing.T) {
	reg := newRegistry(t)
	dir := t.TempDir()
	okPath := writeProjectConfig(t, dir, "clockwork")
	missingPath := writeProjectConfig(t, dir, "torque")
	invalidPath := writeProjectConfig(t, dir, "sigil")
	for _, p := range []string{okPath, missingPath, invalidPath} {
		if _, err := reg.Register(p); err != nil {
			t.Fatalf("Register %s: %v", p, err)
		}
	}

	// Make torque's file disappear and sigil's file unparseable.
	if err := os.Remove(missingPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.WriteFile(invalidPath, []byte("kind: cerberus-project/v1\nowner: sigil\nbogus: ["), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	reports, err := reg.Health()
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	status := map[string]string{}
	for _, r := range reports {
		status[r.Owner] = r.Status
	}
	if status["clockwork"] != HealthOK {
		t.Errorf("clockwork = %q, want %q", status["clockwork"], HealthOK)
	}
	if status["torque"] != HealthMissing {
		t.Errorf("torque = %q, want %q", status["torque"], HealthMissing)
	}
	if status["sigil"] != HealthInvalid {
		t.Errorf("sigil = %q, want %q", status["sigil"], HealthInvalid)
	}
}
