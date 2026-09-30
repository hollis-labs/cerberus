package registry

import (
	"os"
	"path/filepath"
	"testing"
)

const relativeDirConfigYAML = `kind: cerberus-project/v1
owner: portable
project:
  id: portable
  name: Portable
resources:
  - id: api
    name: API
    type: process
    connector: local
    project: portable
    config:
      dir: .
      command: ["./api"]
  - id: gui
    name: GUI
    type: process
    connector: local
    project: portable
    config:
      dir: apps/gui
      command: ["npm", "run", "dev"]
  - id: home
    name: Home
    type: process
    connector: local
    project: portable
    config:
      dir: ~/.cerberus/bin
      command: ["./jaeger"]
  - id: absolute
    name: Absolute
    type: process
    connector: local
    project: portable
    config:
      dir: /opt/absolute
      command: ["./x"]
  - id: remote
    name: Remote
    type: server
    connector: ssh
    project: portable
    config:
      dir: relative-but-not-local
pipelines:
  - id: verify
    name: Verify
    stages:
      - name: test
        actions:
          - type: shell
            dir: .
            command: go test ./...
`

// A registered config names its own repo relatively, so the checkout can live
// anywhere: `dir: .` is the directory the config is in, wherever that is.
func TestRelativeDirResolvesAgainstTheConfigsDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "somewhere", "portable")
	if err := os.MkdirAll(repo, 0o755); err != nil { //nolint:gosec // test repo
		t.Fatal(err)
	}
	path := filepath.Join(repo, "portable.cerberus.yaml")
	if err := os.WriteFile(path, []byte(relativeDirConfigYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	indexPath := filepath.Join(t.TempDir(), DefaultIndexFilename)
	reg := New(indexPath)
	mustRegister(t, reg, path)
	resolved, err := Resolve(ResolveOptions{IndexPath: indexPath})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := map[string]string{
		"api":      repo,
		"gui":      filepath.Join(repo, "apps", "gui"),
		"home":     filepath.Join(home, ".cerberus", "bin"),
		"absolute": "/opt/absolute",
		"remote":   "relative-but-not-local",
	}
	for _, res := range resolved.Config.Resources {
		if got := res.Config["dir"]; got != want[res.ID] {
			t.Errorf("%s: dir = %v, want %s", res.ID, got, want[res.ID])
		}
	}
	if got := resolved.Config.Pipelines[0].Stages[0].Actions[0].Dir; got != repo {
		t.Errorf("pipeline action dir = %q, want %q", got, repo)
	}
}

// Loading through a relative path anchors at the same absolute directory.
func TestRelativeDirFromARelativeConfigPath(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "portable.cerberus.yaml"), []byte(relativeDirConfigYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	pc, err := LoadProjectConfig("portable.cerberus.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// macOS temp dirs sit behind a /var -> /private/var symlink.
	got, _ := filepath.EvalSymlinks(pc.Resources[0].Config["dir"].(string))
	if want, _ := filepath.EvalSymlinks(repo); got != want {
		t.Fatalf("dir = %v, want %s", got, want)
	}
}
