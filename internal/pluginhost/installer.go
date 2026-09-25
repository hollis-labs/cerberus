package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	plugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"gopkg.in/yaml.v3"
)

// PluginYAMLFilename is defined in pkg/plugin so a plugin author can name the
// file without importing internal/.
const PluginYAMLFilename = plugin.PluginYAMLFilename

// DirectoryInstaller installs plugins from a local directory by reading and
// validating the Cerberus-owned plugin.yaml metadata before any subprocess is
// launched.
type DirectoryInstaller struct {
	Policy          InstallPolicy
	SandboxProfile  SandboxProfile
	SandboxEnforced bool

	// ReservedIDs are connector ids the host serves itself, which a plugin may
	// not claim. Empty means nothing is reserved — pluginhost has no opinion
	// about what a host has compiled in, so the caller supplies the set.
	ReservedIDs []string
}

// ReservedIDError reports a plugin refused because its id is already served by
// a built-in connector.
type ReservedIDError struct {
	ID string
}

func (e *ReservedIDError) Error() string {
	return fmt.Sprintf(
		"plugin id %q is a built-in connector and cannot be installed as a plugin: it would shadow the connector Cerberus serves itself. Rename the plugin, or remove the built-in first",
		e.ID)
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

	// Checked before hashing, policy or any subprocess: a plugin that may not
	// be installed at all is refused first.
	//
	// WP-0 made an installed-but-unloaded plugin fall back to the built-in it
	// shadowed, because unloading one left `cerberus docker ps` permanently
	// broken. That is recovery for a state that should not be reachable —
	// refusing the collision at install is the guard, and the fallback stays as
	// the safety net for inventories registered before it existed.
	if i.reserved(spec.ID) {
		return InstalledPlugin{}, &ReservedIDError{ID: spec.ID}
	}

	// The host always computes the entrypoint fingerprint; a caller cannot
	// supply one. It is change detection for later, not a trust signal, and
	// nothing compares it yet (P1, CERB-GAP-336).
	entrypointSHA, err := hashPluginEntrypoint(pluginDir, spec)
	if err != nil {
		return InstalledPlugin{}, err
	}

	decision, err := i.Policy.ValidateInstall(InstallCheck{
		SourcePath:       pluginDir,
		EntrypointSHA256: entrypointSHA,
		SandboxProfile:   i.SandboxProfile,
		SandboxEnforced:  i.SandboxEnforced,
		Manifest:         spec.Cerberus.Connector,
	})
	if err != nil {
		return InstalledPlugin{}, err
	}

	return InstalledPlugin{
		ID:               spec.ID,
		Version:          spec.Version,
		Path:             pluginDir,
		Origin:           decision.Origin,
		Spec:             spec,
		Manifest:         spec.Cerberus.Connector,
		EntrypointSHA256: entrypointSHA,
	}, nil
}

func (i DirectoryInstaller) reserved(id string) bool {
	for _, reserved := range i.ReservedIDs {
		if strings.EqualFold(strings.TrimSpace(reserved), id) {
			return true
		}
	}
	return false
}

// hashPluginEntrypoint returns the SHA-256 of the plugin's entrypoint binary.
// The entrypoint is already validated as a relative path inside the plugin
// directory by PluginYAML.Validate, so it cannot escape via traversal.
func hashPluginEntrypoint(pluginDir string, spec PluginYAML) (string, error) {
	entry := filepath.Join(pluginDir, filepath.FromSlash(spec.Entrypoint.Command))
	file, err := os.Open(entry) //nolint:gosec // validated relative path inside the plugin directory
	if err != nil {
		return "", fmt.Errorf("hash plugin entrypoint %s: %w", entry, err)
	}
	defer file.Close() //nolint:errcheck

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("hash plugin entrypoint %s: %w", entry, err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
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
	// Refused at install rather than declined at load: a plugin asking for a
	// capability this host does not have is an authoring mistake, and telling
	// the operator now beats running it without the access and failing later
	// in a way that looks like a bug.
	if err := ValidateCapabilities(spec.Capabilities); err != nil {
		return PluginYAML{}, fmt.Errorf("plugin %q: %w", spec.ID, err)
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
