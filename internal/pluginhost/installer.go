package pluginhost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const PluginYAMLFilename = "plugin.yaml"

// DirectoryInstaller installs plugins from a local directory by reading and
// validating the Cerberus-owned plugin.yaml metadata before any subprocess is
// launched.
type DirectoryInstaller struct {
	Policy          TrustPolicy
	RequestedTier   TrustTier
	CatalogSigned   bool
	ArchiveSHA256   string
	ArchiveSigned   bool
	SandboxProfile  SandboxProfile
	SandboxEnforced bool
}

var _ Installer = DirectoryInstaller{}

func (i DirectoryInstaller) Install(ctx context.Context, source string) (InstalledPlugin, error) {
	select {
	case <-ctx.Done():
		return InstalledPlugin{}, ctx.Err()
	default:
	}

	pluginDir, err := resolvePluginDir(source)
	if err != nil {
		return InstalledPlugin{}, err
	}
	spec, err := ReadPluginYAML(pluginDir)
	if err != nil {
		return InstalledPlugin{}, err
	}

	policy := i.Policy
	if policy.isZero() {
		policy = DefaultTrustPolicy()
	}

	decision, err := policy.ValidateInstall(TrustCheck{
		SourcePath:      pluginDir,
		CatalogSigned:   i.CatalogSigned,
		ArchiveSHA256:   i.ArchiveSHA256,
		ArchiveSigned:   i.ArchiveSigned,
		LocalPath:       true,
		RequestedTier:   i.RequestedTier,
		SandboxProfile:  i.SandboxProfile,
		SandboxEnforced: i.SandboxEnforced,
		Manifest:        spec.Cerberus.Connector,
	})
	if err != nil {
		return InstalledPlugin{}, err
	}

	return InstalledPlugin{
		ID:       spec.ID,
		Version:  spec.Version,
		Path:     pluginDir,
		Trust:    decision,
		Spec:     spec,
		Manifest: spec.Cerberus.Connector,
	}, nil
}

func ReadPluginYAML(pluginDir string) (PluginYAML, error) {
	if pluginDir == "" {
		return PluginYAML{}, fmt.Errorf("plugin directory is required")
	}

	path := filepath.Join(pluginDir, PluginYAMLFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return PluginYAML{}, fmt.Errorf("read %s: %w", path, err)
	}

	var spec PluginYAML
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return PluginYAML{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := spec.Validate(pluginDir); err != nil {
		return PluginYAML{}, err
	}
	return spec, nil
}

func resolvePluginDir(source string) (string, error) {
	if source == "" {
		return "", fmt.Errorf("plugin source is required")
	}

	path, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("resolve plugin source: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat plugin source: %w", err)
	}
	if info.IsDir() {
		return path, nil
	}
	if filepath.Base(path) != PluginYAMLFilename {
		return "", fmt.Errorf("plugin source %q must be a directory or %s file", source, PluginYAMLFilename)
	}
	return filepath.Dir(path), nil
}

func (p TrustPolicy) isZero() bool {
	return p.Mode == "" &&
		!p.RequireSignature &&
		!p.RequireArchiveHash &&
		!p.RequireArchiveSig &&
		!p.AllowUnsignedLocal &&
		len(p.AllowedDeveloperRoots) == 0
}
