package pluginhost

import (
	"context"
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
