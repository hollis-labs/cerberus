package local

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chrispian/cerberus/internal/domain"
)

// InstallLayout is the default user-area filesystem layout for an os_service
// process resource.
type InstallLayout struct {
	ServiceName  string
	RootDir      string
	CurrentDir   string
	BinDir       string
	ArtifactPath string
	WorkingDir   string
	PlistPath    string
}

// DefaultInstallLayout derives the Cerberus-managed user-area layout for a
// process resource.
func DefaultInstallLayout(homeDir string, res *domain.Resource, spec ProcessSpec) (InstallLayout, error) {
	if homeDir == "" {
		return InstallLayout{}, fmt.Errorf("home directory is required")
	}
	if res == nil {
		return InstallLayout{}, fmt.Errorf("resource is required")
	}
	if res.ID == "" {
		return InstallLayout{}, fmt.Errorf("resource id is required")
	}

	project := sanitizeSlug(res.ProjectID)
	if project == "" {
		project = "default"
	}
	resourceID := sanitizeSlug(res.ID)
	root := spec.InstallRoot
	if root == "" {
		root = filepath.Join(homeDir, ".cerberus", "apps", project, resourceID)
	}

	currentDir := spec.InstallWorkDir
	if currentDir == "" {
		currentDir = filepath.Join(root, "current")
	}
	binDir := filepath.Join(root, "bin")

	artifactPath := spec.ArtifactPath
	if artifactPath == "" {
		artifactPath = filepath.Join(binDir, resourceID)
	}

	serviceName := spec.ServiceName
	if serviceName == "" {
		serviceName = strings.TrimSuffix(
			fmt.Sprintf("com.fragments-engine.cerberus.%s.%s", project, resourceID),
			".",
		)
	}

	workingDir := spec.Dir
	if workingDir == "" {
		workingDir = currentDir
	}

	return InstallLayout{
		ServiceName:  serviceName,
		RootDir:      root,
		CurrentDir:   currentDir,
		BinDir:       binDir,
		ArtifactPath: artifactPath,
		WorkingDir:   workingDir,
		PlistPath:    filepath.Join(homeDir, "Library", "LaunchAgents", serviceName+".plist"),
	}, nil
}

func sanitizeSlug(v string) string {
	if v == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(v))
	lastDot := false
	for _, r := range strings.ToLower(v) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDot = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDot = false
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
			lastDot = r == '.'
		default:
			if !lastDot {
				b.WriteRune('-')
			}
			lastDot = false
		}
	}
	return strings.Trim(b.String(), "-.")
}
