package pluginhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writePluginYAMLFile(t *testing.T, dir string, spec PluginYAML) string {
	t.Helper()

	data := []byte("schema_version: \"1\"\n" +
		"id: " + spec.ID + "\n" +
		"version: " + spec.Version + "\n" +
		"protocol: " + spec.Protocol + "\n" +
		"runtime: " + spec.Runtime + "\n" +
		"entrypoint:\n" +
		"  command: " + spec.Entrypoint.Command + "\n")
	if len(spec.Entrypoint.Args) > 0 {
		data = append(data, []byte("  args:\n")...)
		for _, arg := range spec.Entrypoint.Args {
			data = append(data, []byte("    - "+arg+"\n")...)
		}
	}
	data = append(data, []byte(
		"cerberus:\n"+
			"  connector:\n"+
			"    api_version: "+spec.Cerberus.Connector.APIVersion+"\n"+
			"    kind: "+spec.Cerberus.Connector.Kind+"\n"+
			"    id: "+spec.Cerberus.Connector.ID+"\n"+
			"    version: "+spec.Cerberus.Connector.Version+"\n"+
			"    resource_types:\n"+
			"      - "+spec.Cerberus.Connector.ResourceTypes[0]+"\n"+
			"    capabilities: {}\n")...)
	if secrets := spec.Cerberus.Connector.Config.Secrets; len(secrets) > 0 {
		data = append(data, []byte("    config:\n      secrets:\n")...)
		for _, secret := range secrets {
			data = append(data, []byte(
				"        - name: "+secret.Name+"\n"+
					"          required: "+strconv.FormatBool(secret.Required)+"\n")...)
		}
	}
	data = append(data, []byte(
		"    operations:\n"+
			"      - name: "+spec.Cerberus.Connector.Operations[0].Name+"\n"+
			"        input_schema:\n"+
			"          type: object\n")...)

	path := filepath.Join(dir, PluginYAMLFilename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestDirectoryInstallerInstallsValidatedPlugin(t *testing.T) {
	pluginDir := t.TempDir()
	writeExecutable(t, pluginDir, "bin/docker-plugin")
	spec := testPluginSpec(Entrypoint{Command: "bin/docker-plugin", Args: []string{"--serve"}})
	writePluginYAMLFile(t, pluginDir, spec)

	installer := DirectoryInstaller{
		Policy:        DefaultTrustPolicy(),
		RequestedTier: TrustTierSigned,
		CatalogSigned: true,
		ArchiveSHA256: "abc",
		ArchiveSigned: true,
	}

	installed, err := installer.Install(context.Background(), pluginDir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed.ID != spec.ID || installed.Path != pluginDir {
		t.Fatalf("InstalledPlugin = %+v", installed)
	}
	if installed.Trust.Tier != TrustTierSigned {
		t.Fatalf("Trust tier = %q, want %q", installed.Trust.Tier, TrustTierSigned)
	}
	if installed.Spec.Entrypoint.Command != "bin/docker-plugin" {
		t.Fatalf("Entrypoint = %+v", installed.Spec.Entrypoint)
	}
}

func TestDirectoryInstallerAcceptsPluginYAMLPath(t *testing.T) {
	pluginDir := t.TempDir()
	writeExecutable(t, pluginDir, "bin/docker-plugin")
	spec := testPluginSpec(Entrypoint{Command: "bin/docker-plugin"})
	pluginYAMLPath := writePluginYAMLFile(t, pluginDir, spec)

	installer := DirectoryInstaller{
		Policy:        DefaultTrustPolicy(),
		RequestedTier: TrustTierSigned,
		CatalogSigned: true,
		ArchiveSHA256: "abc",
		ArchiveSigned: true,
	}

	installed, err := installer.Install(context.Background(), pluginYAMLPath)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed.Path != pluginDir {
		t.Fatalf("Installed path = %q, want %q", installed.Path, pluginDir)
	}
}

func TestDirectoryInstallerRejectsInvalidPluginYAML(t *testing.T) {
	pluginDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pluginDir, PluginYAMLFilename), []byte("id: docker\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	installer := DirectoryInstaller{
		Policy:        DefaultTrustPolicy(),
		RequestedTier: TrustTierSigned,
		CatalogSigned: true,
		ArchiveSHA256: "abc",
		ArchiveSigned: true,
	}

	_, err := installer.Install(context.Background(), pluginDir)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("Install error = %v, want validation error", err)
	}
}

func TestDirectoryInstallerRejectsUnsignedPluginInDefaultPolicy(t *testing.T) {
	pluginDir := t.TempDir()
	writeExecutable(t, pluginDir, "bin/docker-plugin")
	spec := testPluginSpec(Entrypoint{Command: "bin/docker-plugin"})
	writePluginYAMLFile(t, pluginDir, spec)

	installer := DirectoryInstaller{
		Policy:        DefaultTrustPolicy(),
		ArchiveSHA256: "abc",
		ArchiveSigned: true,
	}

	_, err := installer.Install(context.Background(), pluginDir)
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("Install error = %v, want signature error", err)
	}
}

// A plugin may not claim an id the host serves itself. WP-0's fallback exists
// to recover from a shadow that already happened; this refuses the shadow.
func TestDirectoryInstallerRefusesAReservedID(t *testing.T) {
	pluginDir := t.TempDir()
	writeExecutable(t, pluginDir, "bin/docker-plugin")
	spec := testPluginSpec(Entrypoint{Command: "bin/docker-plugin"})
	spec.ID = "ssh"
	spec.Cerberus.Connector.ID = "ssh"
	writePluginYAMLFile(t, pluginDir, spec)

	installer := DirectoryInstaller{
		Policy:        DefaultTrustPolicy(),
		RequestedTier: TrustTierSigned,
		CatalogSigned: true,
		ArchiveSigned: true,
		ReservedIDs:   []string{"local", "ssh", "docker", "github"},
	}

	_, err := installer.Install(context.Background(), pluginDir)
	var reserved *ReservedIDError
	if !errors.As(err, &reserved) {
		t.Fatalf("Install error = %v, want a ReservedIDError", err)
	}
	if reserved.ID != "ssh" {
		t.Fatalf("ReservedIDError.ID = %q, want ssh", reserved.ID)
	}
	if !strings.Contains(err.Error(), "shadow") {
		t.Fatalf("error %q should say what the collision would do", err.Error())
	}
}

// Case is not a way around it: ids route operations and are compared as names,
// not as byte strings.
func TestDirectoryInstallerReservedIDIgnoresCase(t *testing.T) {
	pluginDir := t.TempDir()
	writeExecutable(t, pluginDir, "bin/docker-plugin")
	spec := testPluginSpec(Entrypoint{Command: "bin/docker-plugin"})
	spec.ID = "SSH"
	spec.Cerberus.Connector.ID = "SSH"
	writePluginYAMLFile(t, pluginDir, spec)

	installer := DirectoryInstaller{
		Policy:        DefaultTrustPolicy(),
		RequestedTier: TrustTierSigned,
		CatalogSigned: true,
		ArchiveSigned: true,
		ReservedIDs:   []string{"ssh"},
	}
	if _, err := installer.Install(context.Background(), pluginDir); err == nil {
		t.Fatal("a differently-cased built-in id must still be refused")
	}
}

// An id nobody serves installs normally — the guard is a collision check, not a
// gate on plugins in general.
func TestDirectoryInstallerAllowsANonReservedID(t *testing.T) {
	pluginDir := t.TempDir()
	writeExecutable(t, pluginDir, "bin/docker-plugin")
	spec := testPluginSpec(Entrypoint{Command: "bin/docker-plugin"})
	spec.ID = "contextforge"
	spec.Cerberus.Connector.ID = "contextforge"
	writePluginYAMLFile(t, pluginDir, spec)

	installer := DirectoryInstaller{
		Policy:        DefaultTrustPolicy(),
		RequestedTier: TrustTierSigned,
		CatalogSigned: true,
		ArchiveSigned: true,
		ReservedIDs:   []string{"local", "ssh", "docker", "github"},
	}
	installed, err := installer.Install(context.Background(), pluginDir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed.ID != "contextforge" {
		t.Fatalf("ID = %q, want contextforge", installed.ID)
	}
}
