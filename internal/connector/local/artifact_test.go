package local

import (
	"os"
	"os/exec"
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
		homeDir:          func() (string, error) { return tmp, nil },
		now:              func() time.Time { return time.Date(2026, 4, 27, 6, 30, 0, 0, time.UTC) },
		inspectRepoState: inspectArtifactRepoState,
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
		homeDir:          func() (string, error) { return tmp, nil },
		now:              func() time.Time { return time.Date(2026, 4, 27, 6, 30, 0, 0, time.UTC) },
		inspectRepoState: inspectArtifactRepoState,
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
		homeDir:          func() (string, error) { return tmp, nil },
		now:              func() time.Time { return time.Date(2026, 4, 27, 6, 30, 0, 0, time.UTC) },
		inspectRepoState: inspectArtifactRepoState,
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
		homeDir:          func() (string, error) { return tmp, nil },
		now:              func() time.Time { return time.Date(2026, 4, 27, 6, 30, 0, 0, time.UTC) },
		inspectRepoState: inspectArtifactRepoState,
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

// Staleness is driven by source-binary content hash, not by repo metadata.
// Editing source files without rebuilding the binary does NOT mark the
// artifact stale — the source binary on disk is unchanged, so the running
// artifact still matches what was deployed. This is intentional: a docs-only
// commit, an untracked scratch file, or any unrelated repo change should not
// flip every artifact in that repo to "stale". Source code drift becomes
// staleness only after the build runs and produces a different binary.
func TestStatusIgnoresRepoStateWhenSourceBinaryUnchanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(sourceDir, 0755); err != nil { //nolint:gosec
		t.Fatalf("mkdir workspace: %v", err)
	}
	runGit(t, sourceDir, "init")
	runGit(t, sourceDir, "config", "user.email", "test@example.com")
	runGit(t, sourceDir, "config", "user.name", "Test User")

	sourcePath := filepath.Join(sourceDir, "app")
	if err := os.WriteFile(sourcePath, []byte("v1"), 0755); err != nil { //nolint:gosec
		t.Fatalf("write source artifact: %v", err)
	}
	sourceFile := filepath.Join(sourceDir, "main.go")
	if err := os.WriteFile(sourceFile, []byte("package main\n"), 0644); err != nil { //nolint:gosec
		t.Fatalf("write source file: %v", err)
	}
	runGit(t, sourceDir, "add", "app", "main.go")
	runGit(t, sourceDir, "commit", "-m", "initial")

	installer := artifactInstaller{
		homeDir:          func() (string, error) { return tmp, nil },
		now:              func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
		inspectRepoState: inspectArtifactRepoState,
	}
	res := &domain.Resource{ID: "app", ProjectID: "demo"}
	spec := ProcessSpec{
		RunFrom: ProcessRunFromArtifact,
		Dir:     sourceDir,
		Command: []string{"./app", "serve"},
		BuildStrategy: &BuildStrategyConfig{
			Kind: "go_standard",
			Rules: map[string]any{
				"output": "app",
				"target": "./cmd/app",
			},
		},
	}
	if _, err := installer.EnsureInstalled(res, spec); err != nil {
		t.Fatalf("EnsureInstalled failed: %v", err)
	}

	if err := os.WriteFile(sourceFile, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil { //nolint:gosec
		t.Fatalf("rewrite source file: %v", err)
	}
	runGit(t, sourceDir, "commit", "-am", "edit main.go")

	_, status, err := installer.Status(res, spec)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.Stale {
		t.Fatalf("expected stale=false (source binary unchanged), got stale=true reason=%q", status.StaleReason)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // test helper executes fixed git subcommands against a temp repo
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}
