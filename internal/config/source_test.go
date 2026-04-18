package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileSourceSnapshotRereadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	initial := `
version: 1
services:
  - id: svc-a
    name: Service A
    project: proj
    dir: /tmp/a
    command: ["go", "run", "./cmd/a"]
    port: 9001
`
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	src := NewFileSource(path)

	first, err := src.Snapshot()
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if len(first.Services) != 1 || first.Services[0].Command[1] != "run" {
		t.Fatalf("unexpected initial services: %+v", first.Services)
	}

	// Edit the file — the critical case from the 2026-04-18 incident.
	updated := strings.Replace(initial, `["go", "run", "./cmd/a"]`, `["go", "install", "./cmd/a"]`, 1)
	if werr := os.WriteFile(path, []byte(updated), 0600); werr != nil {
		t.Fatal(werr)
	}

	second, err := src.Snapshot()
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if second.Services[0].Command[1] != "install" {
		t.Fatalf("Snapshot returned stale config: %+v", second.Services[0].Command)
	}
}

func TestFileSourceSnapshotReturnsErrorOnInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("::: not yaml :::"), 0600); err != nil {
		t.Fatal(err)
	}

	src := NewFileSource(path)
	_, err := src.Snapshot()
	if err == nil {
		t.Fatal("expected error on invalid YAML, got nil")
	}
	if !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("expected snapshot error wrapper, got %v", err)
	}
}

func TestFileSourceRejectsEmptyPath(t *testing.T) {
	src := NewFileSource("")
	if _, err := src.Snapshot(); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestStaticSourceSetSwaps(t *testing.T) {
	cfg1 := &Config{Version: 1, Services: []ServiceDef{{ID: "a"}}}
	cfg2 := &Config{Version: 1, Services: []ServiceDef{{ID: "b"}}}

	src := NewStaticSource(cfg1)
	got, err := src.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got.Services[0].ID != "a" {
		t.Fatalf("want a, got %s", got.Services[0].ID)
	}

	src.Set(cfg2)
	got, err = src.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got.Services[0].ID != "b" {
		t.Fatalf("want b, got %s", got.Services[0].ID)
	}
}

func TestStaticSourceSnapshotIsolated(t *testing.T) {
	orig := &Config{Version: 1, Services: []ServiceDef{
		{ID: "a", Command: []string{"run"}},
	}}
	src := NewStaticSource(orig)

	got, err := src.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the returned config must not affect subsequent Snapshots.
	got.Services[0].Command[0] = "mutated"

	again, _ := src.Snapshot()
	if again.Services[0].Command[0] != "run" {
		t.Fatalf("static source leaked mutation: %v", again.Services[0].Command)
	}
}

func TestStaticSourceReturnsError(t *testing.T) {
	src := NewStaticSource(&Config{})
	src.SetError(os.ErrPermission)
	if _, err := src.Snapshot(); err == nil {
		t.Fatal("expected error")
	}
}
