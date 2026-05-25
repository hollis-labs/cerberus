package local

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoStandardBuildStrategyMatrixArchivesAndChecksums(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil { //nolint:gosec
		t.Fatalf("mkdir bin: %v", err)
	}
	fakeGo := filepath.Join(binDir, "go")
	if err := os.WriteFile(fakeGo, []byte(`#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    out="$1"
  fi
  shift || true
done
mkdir -p "$(dirname "$out")"
printf 'GOOS=%s GOARCH=%s\n' "$GOOS" "$GOARCH" > "$out"
`), 0755); err != nil { //nolint:gosec
		t.Fatalf("write fake go: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	strategy := goStandardBuildStrategy{}
	result, err := strategy.Build(context.Background(), BuildConfig{
		WorkDir: tmp,
		Env:     []string{"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"), "VERSION=v1.2.3"},
		Source:  map[string]any{"root": "."},
		Rules: map[string]any{
			"target": "./cmd/hadron",
			"matrix": map[string]any{
				"os":   []any{"darwin", "linux"},
				"arch": []any{"amd64", "arm64"},
			},
			"artifacts": map[string]any{
				"name":     "hadron-${VERSION}-${os}-${arch}",
				"archive":  true,
				"checksum": true,
			},
		},
	})
	if err != nil {
		t.Fatalf("Build failed: %v\n%s", err, result.Output)
	}
	if len(result.Artifacts) != 4 {
		t.Fatalf("Artifacts len = %d, want 4", len(result.Artifacts))
	}

	for _, artifact := range result.Artifacts {
		if !strings.HasSuffix(artifact.Path, ".tar.gz") {
			t.Fatalf("artifact %q does not have .tar.gz suffix", artifact.Path)
		}
		if artifact.Checksum == "" {
			t.Fatalf("artifact %q has empty checksum", artifact.Path)
		}
		entry := readSingleTarGzEntry(t, artifact.Path)
		if entry.Name != "hadron" {
			t.Fatalf("archive entry name = %q, want hadron", entry.Name)
		}
		if !strings.Contains(entry.Body, "GOOS="+artifact.Metadata["os"]) {
			t.Fatalf("archive body %q missing GOOS metadata %#v", entry.Body, artifact.Metadata)
		}
		if !strings.Contains(entry.Body, "GOARCH="+artifact.Metadata["arch"]) {
			t.Fatalf("archive body %q missing GOARCH metadata %#v", entry.Body, artifact.Metadata)
		}
	}

	checksums, err := os.ReadFile(filepath.Join(tmp, "dist", "checksums.txt"))
	if err != nil {
		t.Fatalf("read checksums: %v", err)
	}
	for _, want := range []string{
		"hadron-v1.2.3-darwin-amd64.tar.gz",
		"hadron-v1.2.3-darwin-arm64.tar.gz",
		"hadron-v1.2.3-linux-amd64.tar.gz",
		"hadron-v1.2.3-linux-arm64.tar.gz",
	} {
		if !strings.Contains(string(checksums), want) {
			t.Fatalf("checksums.txt missing %q:\n%s", want, string(checksums))
		}
	}
}

type tarEntry struct {
	Name string
	Body string
}

func readSingleTarGzEntry(t *testing.T, path string) tarEntry {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test opens artifact path returned by build strategy
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	header, err := tr.Next()
	if err != nil {
		t.Fatalf("read tar entry: %v", err)
	}
	body, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("read tar body: %v", err)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("expected one archive entry, got err=%v", err)
	}
	return tarEntry{Name: header.Name, Body: string(body)}
}
