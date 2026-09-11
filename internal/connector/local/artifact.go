package local

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
)

const artifactManifestName = "install-manifest.json"

type artifactManifest struct {
	SourcePath   string             `json:"source_path"`
	SourceHash   string             `json:"source_hash"`
	ArtifactPath string             `json:"artifact_path"`
	SyncedAt     time.Time          `json:"synced_at"`
	RepoState    *artifactRepoState `json:"repo_state,omitempty"`
}

type artifactStatus struct {
	Installed    bool
	SourcePath   string
	ArtifactPath string
	SyncedAt     time.Time
	Stale        bool
	StaleReason  string
}

// ArtifactStatus is the exported read-only view of an installed artifact.
type ArtifactStatus = artifactStatus

type artifactSyncResult struct {
	Performed    bool
	Changed      bool
	SourcePath   string
	ArtifactPath string
	SyncedAt     time.Time
}

// ArtifactSyncResult is the exported result of a sync operation.
type ArtifactSyncResult = artifactSyncResult

type artifactInstaller struct {
	homeDir          func() (string, error)
	now              func() time.Time
	inspectRepoState func(ProcessSpec) (*artifactRepoState, error)
}

func newArtifactInstaller() artifactInstaller {
	return artifactInstaller{
		homeDir:          os.UserHomeDir,
		now:              time.Now,
		inspectRepoState: inspectArtifactRepoState,
	}
}

func (i artifactInstaller) EnsureInstalled(res *domain.Resource, spec ProcessSpec) (InstallLayout, error) {
	layout, _, err := i.Sync(res, spec)
	return layout, err
}

func (i artifactInstaller) Sync(res *domain.Resource, spec ProcessSpec) (InstallLayout, artifactSyncResult, error) {
	home, err := i.homeDir()
	if err != nil {
		return InstallLayout{}, artifactSyncResult{}, fmt.Errorf("resolve home dir: %w", err)
	}
	layout, err := DefaultInstallLayout(home, res, spec)
	if err != nil {
		return InstallLayout{}, artifactSyncResult{}, err
	}
	if spec.RunFrom != ProcessRunFromArtifact {
		return layout, artifactSyncResult{}, nil
	}

	sourcePath, err := resolveArtifactSource(spec)
	if err != nil {
		return InstallLayout{}, artifactSyncResult{}, err
	}
	sourceHash, err := fileSHA256(sourcePath)
	if err != nil {
		return InstallLayout{}, artifactSyncResult{}, fmt.Errorf("hash source artifact: %w", err)
	}
	var repoState *artifactRepoState
	if i.inspectRepoState != nil {
		repoState, _ = i.inspectRepoState(spec)
	}

	manifestPath := filepath.Join(layout.RootDir, artifactManifestName)
	if manifest, err := readArtifactManifest(manifestPath); err == nil {
		if manifest.SourcePath == sourcePath && manifest.SourceHash == sourceHash {
			if installedHash, hashErr := fileSHA256(layout.ArtifactPath); hashErr == nil && installedHash == sourceHash {
				return layout, artifactSyncResult{
					Performed:    true,
					Changed:      false,
					SourcePath:   sourcePath,
					ArtifactPath: layout.ArtifactPath,
					SyncedAt:     manifest.SyncedAt,
				}, nil
			}
		}
	}

	if err := os.MkdirAll(layout.BinDir, 0755); err != nil { //nolint:gosec
		return InstallLayout{}, artifactSyncResult{}, fmt.Errorf("create artifact bin dir: %w", err)
	}
	if err := os.MkdirAll(layout.CurrentDir, 0755); err != nil { //nolint:gosec
		return InstallLayout{}, artifactSyncResult{}, fmt.Errorf("create install work dir: %w", err)
	}
	if err := copyFile(sourcePath, layout.ArtifactPath); err != nil {
		return InstallLayout{}, artifactSyncResult{}, fmt.Errorf("copy artifact: %w", err)
	}

	manifest := artifactManifest{
		SourcePath:   sourcePath,
		SourceHash:   sourceHash,
		ArtifactPath: layout.ArtifactPath,
		SyncedAt:     i.now().UTC(),
		RepoState:    repoState,
	}
	if err := writeArtifactManifest(manifestPath, manifest); err != nil {
		return InstallLayout{}, artifactSyncResult{}, fmt.Errorf("write artifact manifest: %w", err)
	}

	return layout, artifactSyncResult{
		Performed:    true,
		Changed:      true,
		SourcePath:   sourcePath,
		ArtifactPath: layout.ArtifactPath,
		SyncedAt:     manifest.SyncedAt,
	}, nil
}

func (i artifactInstaller) Status(res *domain.Resource, spec ProcessSpec) (InstallLayout, artifactStatus, error) {
	home, err := i.homeDir()
	if err != nil {
		return InstallLayout{}, artifactStatus{}, fmt.Errorf("resolve home dir: %w", err)
	}
	layout, err := DefaultInstallLayout(home, res, spec)
	if err != nil {
		return InstallLayout{}, artifactStatus{}, err
	}
	if spec.RunFrom != ProcessRunFromArtifact {
		return layout, artifactStatus{}, nil
	}

	manifestPath := filepath.Join(layout.RootDir, artifactManifestName)
	manifest, err := readArtifactManifest(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return layout, artifactStatus{}, nil
		}
		return InstallLayout{}, artifactStatus{}, err
	}
	if _, statErr := os.Stat(layout.ArtifactPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return layout, artifactStatus{}, nil
		}
		return InstallLayout{}, artifactStatus{}, statErr
	}
	stale, staleReason := i.inspectArtifactDrift(spec, manifest)
	if installedHash, hashErr := fileSHA256(layout.ArtifactPath); hashErr != nil || installedHash != manifest.SourceHash {
		stale, staleReason = true, "installed_artifact_changed"
	}
	return layout, artifactStatus{
		Installed:    true,
		SourcePath:   manifest.SourcePath,
		ArtifactPath: manifest.ArtifactPath,
		SyncedAt:     manifest.SyncedAt,
		Stale:        stale,
		StaleReason:  staleReason,
	}, nil
}

func (i artifactInstaller) inspectArtifactDrift(spec ProcessSpec, manifest artifactManifest) (bool, string) {
	sourcePath, err := resolveArtifactSource(spec)
	if err != nil {
		return true, "source_unresolvable"
	}
	if sourcePath != manifest.SourcePath {
		return true, "source_path_changed"
	}
	sourceHash, err := fileSHA256(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, "source_missing"
		}
		return true, "source_unreadable"
	}
	if sourceHash != manifest.SourceHash {
		return true, "source_changed"
	}
	return false, ""
}

func (i artifactInstaller) Remove(res *domain.Resource, spec ProcessSpec) (InstallLayout, error) {
	home, err := i.homeDir()
	if err != nil {
		return InstallLayout{}, fmt.Errorf("resolve home dir: %w", err)
	}
	layout, err := DefaultInstallLayout(home, res, spec)
	if err != nil {
		return InstallLayout{}, err
	}
	if rmErr := os.RemoveAll(layout.RootDir); rmErr != nil {
		return InstallLayout{}, fmt.Errorf("remove install root: %w", rmErr)
	}
	return layout, nil
}

// InspectArtifactInstall reports the current install state for a local process
// resource without mutating it.
func InspectArtifactInstall(res *domain.Resource, spec ProcessSpec) (InstallLayout, ArtifactStatus, error) {
	return newArtifactInstaller().Status(res, spec)
}

// InspectArtifactInstallBasic is retained as an alias of InspectArtifactInstall
// for callers that historically wanted to skip the (now removed) live git
// repo-drift probe. With staleness driven by source-binary content hash alone,
// both entry points do the same work — no per-poll git invocation either way.
func InspectArtifactInstallBasic(res *domain.Resource, spec ProcessSpec) (InstallLayout, ArtifactStatus, error) {
	return newArtifactInstaller().Status(res, spec)
}

// SyncArtifactInstall performs an artifact sync without starting the runtime.
func SyncArtifactInstall(res *domain.Resource, spec ProcessSpec) (InstallLayout, ArtifactSyncResult, error) {
	return newArtifactInstaller().Sync(res, spec)
}

// ResolveArtifactSourcePath is the exported view of resolveArtifactSource:
// the filesystem path the installer copies into the artifact tree. Callers
// outside this package (e.g. the deploy flow's post-build freshness guard)
// use it to confirm the build actually produced the binary that will be
// installed.
func ResolveArtifactSourcePath(spec ProcessSpec) (string, error) {
	return resolveArtifactSource(spec)
}

func resolveArtifactSource(spec ProcessSpec) (string, error) {
	// Prefer the build strategy's declared output binary. The build writes
	// to that path, so installing from it guarantees the artifact is exactly
	// what the build produced — closing the gap where a build wrote to its
	// declared output while the installer blindly copied a divergent (and
	// possibly stale) command[0]. Resources without a declared output fall
	// back to command[0], preserving existing behavior.
	if out, ok := buildStrategyOutputPath(spec); ok {
		return out, nil
	}
	if len(spec.Command) == 0 {
		return "", fmt.Errorf("artifact mode requires a command with a filesystem path")
	}
	cmd0 := spec.Command[0]
	switch {
	case filepath.IsAbs(cmd0):
		return cmd0, nil
	case strings.ContainsRune(cmd0, filepath.Separator):
		if spec.Dir == "" {
			return "", fmt.Errorf("relative artifact source %q requires process dir", cmd0)
		}
		return filepath.Join(spec.Dir, cmd0), nil
	default:
		return "", fmt.Errorf("artifact mode requires command[0] to be an absolute or relative filesystem path, got %q", cmd0)
	}
}

// buildStrategyOutputPath returns the absolute path of the binary a build
// strategy declares via its `output` rule, resolved against the build dir
// (source.root under the process dir). It returns ok=false when no build
// strategy or no output rule is declared, in which case the caller falls
// back to command[0]. go_standard always declares output; make_standard and
// legacy_command may declare one to make their build/install path explicit.
func buildStrategyOutputPath(spec ProcessSpec) (string, bool) {
	if spec.BuildStrategy == nil {
		return "", false
	}
	out := stringRule(spec.BuildStrategy.Rules, "output", "")
	if out == "" {
		return "", false
	}
	if filepath.IsAbs(out) {
		return out, true
	}
	root := stringRule(spec.BuildStrategy.Source, "root", ".")
	dir := resolveBuildDir(spec.Dir, root)
	if dir == "" {
		return out, true
	}
	return filepath.Join(dir, out), true
}

func readArtifactManifest(path string) (artifactManifest, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from Cerberus-managed install layout
	if err != nil {
		return artifactManifest{}, err
	}
	var manifest artifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return artifactManifest{}, err
	}
	return manifest, nil
}

func writeArtifactManifest(path string, manifest artifactManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact source must be a regular file: %s", src)
	}
	// A fresh inode avoids macOS's cached code-signing identity. Rename also
	// keeps readers on the previous complete binary until the copy is ready.
	out, err := os.CreateTemp(filepath.Dir(dst), ".artifact-*")
	if err != nil {
		return err
	}
	defer func() { _ = out.Close(); _ = os.Remove(out.Name()) }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), dst)
}

// artifactRepoState captures the source repo's git state at sync time. It is
// recorded in install-manifest.json for operator inspection ("what HEAD was
// this artifact built from?"), but is NOT used to compute staleness — the
// content-hash check on the source binary is authoritative. Any commit or
// untracked-file change in the source repo would otherwise mark every artifact
// in that repo stale, even when the resource's own binary is byte-identical.
type artifactRepoState struct {
	RepoRoot    string `json:"repo_root,omitempty"`
	GitHead     string `json:"git_head,omitempty"`
	GitDirty    bool   `json:"git_dirty,omitempty"`
	GitTreeHash string `json:"git_tree_hash,omitempty"`
}

func inspectArtifactRepoState(spec ProcessSpec) (*artifactRepoState, error) {
	if spec.Dir == "" || !HasBuildStrategy(spec) {
		return nil, nil
	}
	root, err := gitOutput(spec.Dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	head, err := gitOutput(spec.Dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	status, err := gitOutput(spec.Dir, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	return &artifactRepoState{
		RepoRoot:    root,
		GitHead:     head,
		GitDirty:    status != "",
		GitTreeHash: hashString(status),
	}, nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...) //nolint:gosec // arguments are fixed internal git queries, not user shell input
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func hashString(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
