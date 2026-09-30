package local

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/cerberus/internal/domain"
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

	// UnitName and UnitPath are the systemd user unit the resource installs
	// as under supervisor systemd_user: the launchd label with ".service"
	// appended, so one resource has one name on every platform
	// (com.hollis-labs.cerberus.<project>.<resource>.service), in the user
	// manager's own unit directory, ~/.config/systemd/user.
	UnitName string
	UnitPath string

	// LegacyServiceName and LegacyPlistPath are where a resource with a
	// derived service name was installed before the label prefix was
	// renamed. Empty when the resource names its own service. Apply moves a
	// resource off them, and Remove clears them.
	LegacyServiceName string
	LegacyPlistPath   string
}

// ServicePrefix is the launchd label prefix of a resource whose service name
// is derived; LegacyServicePrefix is the one it replaced.
const (
	ServicePrefix       = "com.hollis-labs.cerberus"
	LegacyServicePrefix = "com.fragments-engine.cerberus"
)

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

	serviceName, legacyName := spec.ServiceName, ""
	if serviceName == "" {
		serviceName = strings.TrimSuffix(fmt.Sprintf("%s.%s.%s", ServicePrefix, project, resourceID), ".")
		legacyName = strings.TrimSuffix(fmt.Sprintf("%s.%s.%s", LegacyServicePrefix, project, resourceID), ".")
	}

	workingDir := spec.Dir
	if workingDir == "" {
		workingDir = currentDir
	}

	layout := InstallLayout{
		ServiceName:  serviceName,
		RootDir:      root,
		CurrentDir:   currentDir,
		BinDir:       binDir,
		ArtifactPath: artifactPath,
		WorkingDir:   workingDir,
		PlistPath:    filepath.Join(homeDir, "Library", "LaunchAgents", serviceName+".plist"),
		UnitName:     SystemdUnitName(serviceName),
	}
	layout.UnitPath = filepath.Join(SystemdUserUnitDir(homeDir), layout.UnitName)
	if legacyName != "" {
		layout.LegacyServiceName = legacyName
		layout.LegacyPlistPath = filepath.Join(homeDir, "Library", "LaunchAgents", legacyName+".plist")
	}
	return layout, nil
}

// SystemdUnitName is the systemd user unit a service name installs as. A
// service_name that already ends in ".service" is taken as the unit name.
func SystemdUnitName(serviceName string) string {
	return strings.TrimSuffix(serviceName, ".service") + ".service"
}

// SystemdUserUnitDir is where Cerberus writes systemd user units:
// ~/.config/systemd/user, the directory `systemctl --user` reads for units an
// administrator did not install system-wide.
func SystemdUserUnitDir(homeDir string) string {
	return filepath.Join(homeDir, ".config", "systemd", "user")
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
