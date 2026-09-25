package pluginhost

import (
	"strings"
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func validManifest() contract.Manifest {
	return contract.Manifest{
		APIVersion:    contract.ManifestAPIVersion,
		Kind:          "Connector",
		ID:            "docker",
		Version:       "dev",
		ResourceTypes: []string{"container"},
		Operations: []contract.ManifestOperation{
			{Name: "status", InputSchema: contract.ObjectSchema(map[string]any{})},
		},
	}
}

func TestDeveloperInstallPolicyAllowsPluginUnderRoot(t *testing.T) {
	if !DevModeEnabled {
		t.Skip("development installs require a devmode build")
	}
	root := t.TempDir()
	decision, err := DeveloperInstallPolicy(root).ValidateInstall(InstallCheck{
		SourcePath:       root + "/plugins/docker",
		EntrypointSHA256: "abc",
		Manifest:         validManifest(),
	})
	if err != nil {
		t.Fatalf("ValidateInstall: %v", err)
	}
	if decision.Origin != OriginDev {
		t.Fatalf("Origin = %q, want %q", decision.Origin, OriginDev)
	}
}

func TestDeveloperInstallPolicyRejectsPluginOutsideRoot(t *testing.T) {
	if !DevModeEnabled {
		t.Skip("development installs require a devmode build")
	}
	root := t.TempDir()
	other := t.TempDir()
	_, err := DeveloperInstallPolicy(root).ValidateInstall(InstallCheck{
		SourcePath:       other + "/docker",
		EntrypointSHA256: "abc",
		Manifest:         validManifest(),
	})
	if err == nil || !strings.Contains(err.Error(), "outside allowed developer roots") {
		t.Fatalf("error = %v, want root validation", err)
	}
}

func TestDeveloperInstallPolicyRejectedInProductionBuild(t *testing.T) {
	if DevModeEnabled {
		t.Skip("production-only assertion")
	}
	root := t.TempDir()
	_, err := DeveloperInstallPolicy(root).ValidateInstall(InstallCheck{
		SourcePath:       root + "/plugins/docker",
		EntrypointSHA256: "abc",
		Manifest:         validManifest(),
	})
	if err == nil || !strings.Contains(err.Error(), "devmode build") {
		t.Fatalf("error = %v, want devmode build", err)
	}
}

// A local install works in an ordinary build and records origin installed.
func TestLocalInstallPolicyRecordsInstalled(t *testing.T) {
	decision, err := LocalInstallPolicy().ValidateInstall(InstallCheck{
		SourcePath:       "/plugins/contextforge",
		EntrypointSHA256: "abc123",
		Manifest:         validManifest(),
	})
	if err != nil {
		t.Fatalf("ValidateInstall returned %v, want nil", err)
	}
	if decision.Origin != OriginInstalled {
		t.Fatalf("Origin = %q, want %q", decision.Origin, OriginInstalled)
	}
}

// The fingerprint is host-computed; an install without one is refused.
func TestInstallRequiresEntrypointFingerprint(t *testing.T) {
	_, err := LocalInstallPolicy().ValidateInstall(InstallCheck{Manifest: validManifest()})
	if err == nil || !strings.Contains(err.Error(), "fingerprinted") {
		t.Fatalf("error = %v, want fingerprint error", err)
	}
}

func TestSandboxProfileMustBeEnforced(t *testing.T) {
	_, err := LocalInstallPolicy().ValidateInstall(InstallCheck{
		EntrypointSHA256: "abc",
		SandboxProfile:   SandboxProfileDockerHost,
		Manifest:         validManifest(),
	})
	if err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("error = %v, want sandbox", err)
	}
}

// An installed plugin can run destructive operations, with acknowledgment:
// the plugin lane has to be able to carry provider integrations.
func TestOperationAllowedInstalledDestructiveNeedsAck(t *testing.T) {
	op := contract.ManifestOperation{Name: "destroy", Destructive: true, RequiresAck: true}
	if err := OperationAllowed(OriginInstalled, op, false); err == nil || !strings.Contains(err.Error(), "acknowledgment") {
		t.Fatalf("error = %v, want acknowledgment", err)
	}
	if err := OperationAllowed(OriginInstalled, op, true); err != nil {
		t.Fatalf("acknowledged destructive op rejected: %v", err)
	}
	if err := OperationAllowed(OriginInstalled, contract.ManifestOperation{Name: "list"}, false); err != nil {
		t.Fatalf("read op rejected: %v", err)
	}
}

// A development install is a restriction: destructive operations are refused
// even with acknowledgment.
func TestOperationAllowedRefusesDevDestructive(t *testing.T) {
	op := contract.ManifestOperation{Name: "destroy", Destructive: true, RequiresAck: true}
	err := OperationAllowed(OriginDev, op, true)
	if err == nil || !strings.Contains(err.Error(), "development (--dev) plugin") {
		t.Fatalf("error = %v, want dev destructive refusal", err)
	}
	if err := OperationAllowed(OriginDev, contract.ManifestOperation{Name: "list"}, false); err != nil {
		t.Fatalf("dev read op rejected: %v", err)
	}
}

func TestOperationAllowedRefusesUnknownOrigin(t *testing.T) {
	for _, origin := range []InstallOrigin{"", "signed", "unsigned"} {
		if err := OperationAllowed(origin, contract.ManifestOperation{Name: "list"}, true); err == nil {
			t.Errorf("origin %q allowed", origin)
		}
	}
}
