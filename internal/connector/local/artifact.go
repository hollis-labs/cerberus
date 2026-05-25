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
	homeDir              func() (string, error)
	now                  func() time.Time
	inspectRepoState     func(ProcessSpec) (*artifactRepoState, error)
	skipCurrentRepoState bool
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
		repoStateCurrent := repoStateEqual(manifest.RepoState, repoState)
		if manifest.SourcePath == sourcePath && manifest.SourceHash == sourceHash && repoStateCurrent {
			if _, statErr := os.Stat(layout.ArtifactPath); statErr == nil {
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
	if !i.skipCurrentRepoState && manifest.RepoState != nil {
		currentRepoState, repoErr := inspectArtifactRepoState(spec)
		if repoErr == nil {
			if stale, reason := manifest.RepoState.diff(currentRepoState); stale {
				return true, reason
			}
		}
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

// InspectArtifactInstallBasic reports artifact install state without running
// live repository drift probes. Use this for high-fanout polling surfaces.
func InspectArtifactInstallBasic(res *domain.Resource, spec ProcessSpec) (InstallLayout, ArtifactStatus, error) {
	installer := newArtifactInstaller()
	installer.skipCurrentRepoState = true
	return installer.Status(res, spec)
}

// SyncArtifactInstall performs an artifact sync without starting the runtime.
func SyncArtifactInstall(res *domain.Resource, spec ProcessSpec) (InstallLayout, ArtifactSyncResult, error) {
	return newArtifactInstaller().Sync(res, spec)
}

func resolveArtifactSource(spec ProcessSpec) (string, error) {
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

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm()) //nolint:gosec
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

type artifactRepoState struct {
	RepoRoot    string `json:"repo_root,omitempty"`
	GitHead     string `json:"git_head,omitempty"`
	GitDirty    bool   `json:"git_dirty,omitempty"`
	GitTreeHash string `json:"git_tree_hash,omitempty"`
}

func (s artifactRepoState) diff(other *artifactRepoState) (bool, string) {
	if other == nil {
		return false, ""
	}
	if s.RepoRoot != "" && other.RepoRoot != "" && s.RepoRoot != other.RepoRoot {
		return true, "repo_root_changed"
	}
	if s.GitHead != "" && other.GitHead != "" && s.GitHead != other.GitHead {
		return true, "repo_head_changed"
	}
	if s.GitDirty != other.GitDirty {
		return true, "repo_worktree_changed"
	}
	if s.GitTreeHash != "" && other.GitTreeHash != "" && s.GitTreeHash != other.GitTreeHash {
		return true, "repo_worktree_changed"
	}
	return false, ""
}

func repoStateEqual(left, right *artifactRepoState) bool {
	switch {
	case left == nil && right == nil:
		return true
	case left == nil || right == nil:
		return false
	default:
		return left.RepoRoot == right.RepoRoot &&
			left.GitHead == right.GitHead &&
			left.GitDirty == right.GitDirty &&
			left.GitTreeHash == right.GitTreeHash
	}
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
