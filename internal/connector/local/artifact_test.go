package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
)

func TestResolveArtifactSource(t *testing.T) {
	got, err := resolveArtifactSource(ProcessSpec{
		Dir:     "/tmp/app",
		Command: []string{"./bin/app", "serve"},
	})
	if err != nil {
		t.Fatalf("resolveArtifactSource failed: %v", err)
	}
	if got != filepath.Join("/tmp/app", "./bin/app") {
		t.Fatalf("got %q", got)
	}
}

func TestResolveArtifactSourceRejectsBareBinary(t *testing.T) {
	_, err := resolveArtifactSource(ProcessSpec{
		Dir:     "/tmp/app",
		Command: []string{"app", "serve"},
	})
	if err == nil || !strings.Contains(err.Error(), "filesystem path") {
		t.Fatalf("expected filesystem-path error, got %v", err)
	}
}

func TestEnsureInstalledCopiesArtifactAndWritesManifest(t *testing.T) {
	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(sourceDir, 0755); err != nil { //nolint:gosec
		t.Fatalf("mkdir workspace: %v", err)
	}
	sourcePath := filepath.Join(sourceDir, "app")
	if err := os.WriteFile(sourcePath, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatalf("write source artifact: %v", err)
	}

	installer := artifactInstaller{
		homeDir: func() (string, error) { return tmp, nil },
		now:     func() time.Time { return time.Date(2026, 4, 27, 6, 30, 0, 0, time.UTC) },
	}
	layout, err := installer.EnsureInstalled(&domain.Resource{
		ID:        "app",
		ProjectID: "demo",
	}, ProcessSpec{
		RunFrom: ProcessRunFromArtifact,
		Dir:     sourceDir,
		Command: []string{"./app", "serve"},
	})
	if err != nil {
		t.Fatalf("EnsureInstalled failed: %v", err)
	}

	data, err := os.ReadFile(layout.ArtifactPath)
	if err != nil {
		t.Fatalf("read installed artifact: %v", err)
	}
	if string(data) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("unexpected installed artifact contents: %q", string(data))
	}

	manifestPath := filepath.Join(layout.RootDir, artifactManifestName)
	manifest, err := readArtifactManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.SourcePath != sourcePath {
		t.Fatalf("manifest.SourcePath = %q, want %q", manifest.SourcePath, sourcePath)
	}
}

func TestEnsureInstalledSkipsUnchangedArtifact(t *testing.T) {
	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(sourceDir, 0755); err != nil { //nolint:gosec
		t.Fatalf("mkdir workspace: %v", err)
	}
	sourcePath := filepath.Join(sourceDir, "app")
	if err := os.WriteFile(sourcePath, []byte("v1"), 0755); err != nil { //nolint:gosec
		t.Fatalf("write source artifact: %v", err)
	}

	installer := artifactInstaller{
		homeDir: func() (string, error) { return tmp, nil },
		now:     func() time.Time { return time.Date(2026, 4, 27, 6, 30, 0, 0, time.UTC) },
	}
	res := &domain.Resource{ID: "app", ProjectID: "demo"}
	spec := ProcessSpec{
		RunFrom: ProcessRunFromArtifact,
		Dir:     sourceDir,
		Command: []string{"./app", "serve"},
	}
	layout, err := installer.EnsureInstalled(res, spec)
	if err != nil {
		t.Fatalf("first EnsureInstalled failed: %v", err)
	}
	firstInfo, err := os.Stat(layout.ArtifactPath)
	if err != nil {
		t.Fatalf("stat installed artifact: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	layout, err = installer.EnsureInstalled(res, spec)
	if err != nil {
		t.Fatalf("second EnsureInstalled failed: %v", err)
	}
	secondInfo, err := os.Stat(layout.ArtifactPath)
	if err != nil {
		t.Fatalf("stat installed artifact: %v", err)
	}

	if !firstInfo.ModTime().Equal(secondInfo.ModTime()) {
		t.Fatalf("expected unchanged artifact modtime, got %v then %v", firstInfo.ModTime(), secondInfo.ModTime())
	}
}

func TestInspectArtifactInstallDetectsChangedSource(t *testing.T) {
	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(sourceDir, 0755); err != nil { //nolint:gosec
		t.Fatalf("mkdir workspace: %v", err)
	}
	sourcePath := filepath.Join(sourceDir, "app")
	if err := os.WriteFile(sourcePath, []byte("v1"), 0755); err != nil { //nolint:gosec
		t.Fatalf("write source artifact: %v", err)
	}

	installer := artifactInstaller{
		homeDir: func() (string, error) { return tmp, nil },
		now:     func() time.Time { return time.Date(2026, 4, 27, 6, 30, 0, 0, time.UTC) },
	}
	res := &domain.Resource{ID: "app", ProjectID: "demo"}
	spec := ProcessSpec{
		RunFrom: ProcessRunFromArtifact,
		Dir:     sourceDir,
		Command: []string{"./app", "serve"},
	}
	if _, err := installer.EnsureInstalled(res, spec); err != nil {
		t.Fatalf("EnsureInstalled failed: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("v2"), 0755); err != nil { //nolint:gosec
		t.Fatalf("rewrite source artifact: %v", err)
	}

	_, status, err := installer.Status(res, spec)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !status.Installed {
		t.Fatalf("expected installed=true")
	}
	if !status.Stale {
		t.Fatalf("expected stale=true")
	}
	if status.StaleReason != "source_changed" {
		t.Fatalf("stale reason = %q, want %q", status.StaleReason, "source_changed")
	}
}

func TestInspectArtifactInstallDetectsMissingSource(t *testing.T) {
	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(sourceDir, 0755); err != nil { //nolint:gosec
		t.Fatalf("mkdir workspace: %v", err)
	}
	sourcePath := filepath.Join(sourceDir, "app")
	if err := os.WriteFile(sourcePath, []byte("v1"), 0755); err != nil { //nolint:gosec
		t.Fatalf("write source artifact: %v", err)
	}

	installer := artifactInstaller{
		homeDir: func() (string, error) { return tmp, nil },
		now:     func() time.Time { return time.Date(2026, 4, 27, 6, 30, 0, 0, time.UTC) },
	}
	res := &domain.Resource{ID: "app", ProjectID: "demo"}
	spec := ProcessSpec{
		RunFrom: ProcessRunFromArtifact,
		Dir:     sourceDir,
		Command: []string{"./app", "serve"},
	}
	if _, err := installer.EnsureInstalled(res, spec); err != nil {
		t.Fatalf("EnsureInstalled failed: %v", err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("remove source artifact: %v", err)
	}

	_, status, err := installer.Status(res, spec)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !status.Installed {
		t.Fatalf("expected installed=true")
	}
	if !status.Stale {
		t.Fatalf("expected stale=true")
	}
	if status.StaleReason != "source_missing" {
		t.Fatalf("stale reason = %q, want %q", status.StaleReason, "source_missing")
	}
}
