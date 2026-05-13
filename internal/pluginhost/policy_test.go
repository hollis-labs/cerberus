package pluginhost

import (
	"strings"
	"testing"

	contract "github.com/chrispian/cerberus/pkg/connector"
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

func TestDefaultTrustPolicyRequiresSignature(t *testing.T) {
	_, err := DefaultTrustPolicy().ValidateInstall(TrustCheck{
		SourcePath:    "/tmp/docker",
		ArchiveSHA256: "abc",
		ArchiveSigned: true,
		Manifest:      validManifest(),
	})
	if err == nil {
		t.Fatal("expected signature error")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Fatalf("error = %v, want signature", err)
	}
}

func TestDeveloperTrustPolicyAllowsUnsignedPluginUnderRoot(t *testing.T) {
	if !DevModeEnabled {
		t.Skip("developer trust policy requires devmode build")
	}
	root := t.TempDir()
	decision, err := DeveloperTrustPolicy(root).ValidateInstall(TrustCheck{
		SourcePath:    root + "/plugins/docker",
		ArchiveSHA256: "abc",
		LocalPath:     true,
		Manifest:      validManifest(),
	})
	if err != nil {
		t.Fatalf("ValidateInstall: %v", err)
	}
	if decision.Tier != TrustTierUnsignedDev {
		t.Fatalf("Tier = %q, want %q", decision.Tier, TrustTierUnsignedDev)
	}
}

func TestDeveloperTrustPolicyRejectsUnsignedPluginOutsideRoot(t *testing.T) {
	if !DevModeEnabled {
		t.Skip("developer trust policy requires devmode build")
	}
	root := t.TempDir()
	other := t.TempDir()
	_, err := DeveloperTrustPolicy(root).ValidateInstall(TrustCheck{
		SourcePath:    other + "/docker",
		ArchiveSHA256: "abc",
		LocalPath:     true,
		Manifest:      validManifest(),
	})
	if err == nil {
		t.Fatal("expected root validation error")
	}
	if !strings.Contains(err.Error(), "outside allowed developer roots") {
		t.Fatalf("error = %v, want root validation", err)
	}
}

func TestDeveloperTrustPolicyRejectedInProductionBuild(t *testing.T) {
	if DevModeEnabled {
		t.Skip("production-only assertion")
	}
	root := t.TempDir()
	_, err := DeveloperTrustPolicy(root).ValidateInstall(TrustCheck{
		SourcePath:    root + "/plugins/docker",
		ArchiveSHA256: "abc",
		LocalPath:     true,
		Manifest:      validManifest(),
	})
	if err == nil {
		t.Fatal("expected devmode build error")
	}
	if !strings.Contains(err.Error(), "devmode build") {
		t.Fatalf("error = %v, want devmode build", err)
	}
}

func TestDefaultTrustPolicyRequiresArchiveHashAndSignature(t *testing.T) {
	_, err := DefaultTrustPolicy().ValidateInstall(TrustCheck{
		CatalogSigned: true,
		ArchiveSigned: true,
		Manifest:      validManifest(),
	})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("error = %v, want sha256", err)
	}

	_, err = DefaultTrustPolicy().ValidateInstall(TrustCheck{
		CatalogSigned: true,
		ArchiveSHA256: "abc",
		Manifest:      validManifest(),
	})
	if err == nil || !strings.Contains(err.Error(), "archive signature") {
		t.Fatalf("error = %v, want archive signature", err)
	}
}

func TestSandboxProfileMustBeEnforced(t *testing.T) {
	_, err := DefaultTrustPolicy().ValidateInstall(TrustCheck{
		CatalogSigned:  true,
		ArchiveSHA256:  "abc",
		ArchiveSigned:  true,
		SandboxProfile: SandboxProfileDockerHost,
		Manifest:       validManifest(),
	})
	if err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("error = %v, want sandbox", err)
	}
}

func TestOperationAllowedRequiresAckForSignedDestructive(t *testing.T) {
	op := contract.ManifestOperation{Name: "destroy", Destructive: true, RequiresAck: true}
	err := OperationAllowed(TrustTierSigned, op, false)
	if err == nil || !strings.Contains(err.Error(), "acknowledgment") {
		t.Fatalf("error = %v, want acknowledgment", err)
	}
	if err := OperationAllowed(TrustTierSigned, op, true); err != nil {
		t.Fatalf("OperationAllowed signed acknowledged: %v", err)
	}
}

func TestOperationAllowedRejectsDevDestructive(t *testing.T) {
	op := contract.ManifestOperation{Name: "destroy", Destructive: true, RequiresAck: true}
	err := OperationAllowed(TrustTierLocalDev, op, true)
	if err == nil || !strings.Contains(err.Error(), "not agent-auto executable") {
		t.Fatalf("error = %v, want dev destructive rejection", err)
	}
}
