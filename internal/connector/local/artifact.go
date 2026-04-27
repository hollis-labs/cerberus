package local

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
)

const artifactManifestName = "install-manifest.json"

type artifactManifest struct {
	SourcePath   string    `json:"source_path"`
	SourceHash   string    `json:"source_hash"`
	ArtifactPath string    `json:"artifact_path"`
	SyncedAt     time.Time `json:"synced_at"`
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
	homeDir func() (string, error)
	now     func() time.Time
}

func newArtifactInstaller() artifactInstaller {
	return artifactInstaller{
		homeDir: os.UserHomeDir,
		now:     time.Now,
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

	manifestPath := filepath.Join(layout.RootDir, artifactManifestName)
	if manifest, err := readArtifactManifest(manifestPath); err == nil {
		if manifest.SourcePath == sourcePath && manifest.SourceHash == sourceHash {
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
	stale, staleReason := inspectArtifactDrift(spec, manifest)
	return layout, artifactStatus{
		Installed:    true,
		SourcePath:   manifest.SourcePath,
		ArtifactPath: manifest.ArtifactPath,
		SyncedAt:     manifest.SyncedAt,
		Stale:        stale,
		StaleReason:  staleReason,
	}, nil
}

func inspectArtifactDrift(spec ProcessSpec, manifest artifactManifest) (bool, string) {
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
