package local

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/domain"
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
	// cmd.Dir does NOT win against GIT_DIR. Git exports GIT_DIR, GIT_WORK_TREE
	// and GIT_INDEX_FILE into hook environments, and lefthook's pre-push runs
	// `go test ./...` — so without this, every git call below operates on the
	// developer's real repository instead of the temp one. That is not
	// hypothetical: it rewrote this repo's user.name/user.email to the fixture
	// identity and committed "initial" and "edit main.go" onto whatever branch
	// was being pushed, the second of which deletes every tracked file because
	// `commit -am` stages the absence of a temp fixture's files from the real
	// work tree.
	cmd.Env = gitScrubbedEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

// gitScrubbedEnv returns the environment with every GIT_* variable removed, so
// a git subprocess is directed only by cmd.Dir. Dropping the whole prefix
// rather than a known list because the set grows between git versions, and the
// failure mode of missing one is silent corruption of the caller's repository.
func gitScrubbedEnv() []string {
	parent := os.Environ()
	out := make([]string, 0, len(parent))
	for _, entry := range parent {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

//nolint:gosec // executable fixtures and file paths are confined to t.TempDir
func TestCopyArtifactReplacesInodeAndPreservesOpenReaders(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "source"), filepath.Join(dir, "installed")
	if err := os.WriteFile(src, []byte("new binary"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old binary"), 0700); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	before, err := old.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err = copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("replacement reused installed inode")
	}
	if after.Mode().Perm() != 0700 {
		t.Fatalf("lost executable mode: %v", after.Mode())
	}
	oldBytes := make([]byte, 10)
	if _, err = old.Read(oldBytes); err != nil {
		t.Fatal(err)
	}
	newBytes, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(oldBytes) != "old binary" || string(newBytes) != "new binary" {
		t.Fatal("reader observed partial replacement")
	}
	if err = copyFile(dir, dst); err == nil {
		t.Fatal("directory source accepted")
	}
	newBytes, err = os.ReadFile(dst)
	if err != nil || string(newBytes) != "new binary" {
		t.Fatalf("failed copy damaged destination: %q %v", newBytes, err)
	}
}

//nolint:gosec // executable fixtures are confined to t.TempDir
func TestSyncRepairsTamperedInstalledArtifact(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	if err := os.WriteFile(src, []byte("valid"), 0700); err != nil {
		t.Fatal(err)
	}
	installer := artifactInstaller{homeDir: func() (string, error) { return dir, nil }, now: time.Now}
	res := &domain.Resource{ID: "app", ProjectID: "demo"}
	spec := ProcessSpec{RunFrom: ProcessRunFromArtifact, Command: []string{src}}
	layout, _, err := installer.Sync(res, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(layout.ArtifactPath, []byte("corrupt"), 0700); err != nil {
		t.Fatal(err)
	}
	_, status, err := installer.Status(res, spec)
	if err != nil || !status.Stale {
		t.Fatalf("corruption not detected: %+v %v", status, err)
	}
	_, result, err := installer.Sync(res, spec)
	if err != nil || !result.Changed {
		t.Fatalf("corruption not repaired: %+v %v", result, err)
	}
	installedHash, err := fileSHA256(layout.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash, err := fileSHA256(src)
	if err != nil || installedHash != sourceHash {
		t.Fatal("repair did not restore artifact")
	}
}

// A test that shells out to git must not be steerable by the environment it
// inherits. Git exports GIT_DIR into hook environments and lefthook's pre-push
// runs the suite, so an unscrubbed helper commits into the repository being
// pushed. This asserts the helper ignores a hostile GIT_DIR rather than
// trusting that no caller sets one.
func TestRunGitIgnoresAmbientGitDir(t *testing.T) {
	victim := t.TempDir()
	runGit(t, victim, "init")
	runGit(t, victim, "config", "user.email", "victim@example.com")
	runGit(t, victim, "config", "user.name", "Victim")

	// Point GIT_DIR at the victim, then operate somewhere else entirely.
	t.Setenv("GIT_DIR", filepath.Join(victim, ".git"))
	t.Setenv("GIT_WORK_TREE", victim)

	work := t.TempDir()
	runGit(t, work, "init")
	runGit(t, work, "config", "user.email", "test@example.com")
	runGit(t, work, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(work, "app"), []byte("v1"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	runGit(t, work, "add", "app")
	runGit(t, work, "commit", "-m", "initial")

	// The victim must be untouched: no commit, and its identity intact.
	log := exec.Command("git", "log", "--oneline")
	log.Dir = victim
	log.Env = gitScrubbedEnv()
	if out, err := log.CombinedOutput(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		t.Fatalf("the ambient GIT_DIR repository received commits: %s", out)
	}

	cfg := exec.Command("git", "config", "--get", "user.email")
	cfg.Dir = victim
	cfg.Env = gitScrubbedEnv()
	out, err := cfg.CombinedOutput()
	if err != nil {
		t.Fatalf("read victim config: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "victim@example.com" {
		t.Fatalf("the ambient GIT_DIR repository's identity was rewritten to %q", got)
	}
}
