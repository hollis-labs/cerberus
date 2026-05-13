package dockerplugin

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/pluginhost"
)

func TestPluginYAMLValidates(t *testing.T) {
	spec := PluginYAML()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "bin", BinaryName)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := spec.Validate(dir); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestWritePrototypeWritesPluginYAML(t *testing.T) {
	dir := t.TempDir()
	if err := WritePrototype(dir); err != nil {
		t.Fatalf("WritePrototype: %v", err)
	}

	path := filepath.Join(dir, pluginhost.PluginYAMLFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"id: docker",
		"protocol: plugin-sdk/subprocess",
		"command: bin/cerberus-docker-plugin",
		"name: destroy",
		"requires_ack: true",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plugin.yaml missing %q\n%s", want, text)
		}
	}
}

func TestWritePrototypeWithBinaryBuildsExecutable(t *testing.T) {
	dir := t.TempDir()
	if err := WritePrototypeWithBinary(context.Background(), dir, repoRoot(t)); err != nil {
		t.Fatalf("WritePrototypeWithBinary: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "bin", BinaryName))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("binary mode = %v, want executable", info.Mode())
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
