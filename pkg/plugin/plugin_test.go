package plugin

import (
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func TestToolNameForOperation(t *testing.T) {
	if got := ToolNameForOperation("docker", "logs"); got != "cerberus_docker_logs" {
		t.Fatalf("ToolNameForOperation = %q, want cerberus_docker_logs", got)
	}
}

func TestOperationFromToolName(t *testing.T) {
	manifest := contract.Manifest{
		Operations: []contract.ManifestOperation{{Name: "status"}, {Name: "logs"}},
	}

	op, ok := OperationFromToolName("docker", "cerberus_docker_logs", manifest)
	if !ok {
		t.Fatal("expected operation")
	}
	if op.Name != "logs" {
		t.Fatalf("Name = %q, want logs", op.Name)
	}

	if _, ok := OperationFromToolName("docker", "cerberus_docker_missing", manifest); ok {
		t.Fatal("expected no operation for an unknown tool name")
	}
}

func TestSecretFromConfig(t *testing.T) {
	config := map[string]string{"token": "jwt-value", "empty": ""}

	if value, ok := SecretFromConfig(config, "token"); !ok || value != "jwt-value" {
		t.Fatalf("SecretFromConfig(token) = %q, %v", value, ok)
	}
	// A secret the host could not resolve reads as absent, not as an empty
	// credential a plugin might send to a provider.
	if _, ok := SecretFromConfig(config, "empty"); ok {
		t.Fatal("an empty value must read as absent")
	}
	if _, ok := SecretFromConfig(config, "missing"); ok {
		t.Fatal("an undeclared secret must read as absent")
	}
	if _, ok := SecretFromConfig(nil, "token"); ok {
		t.Fatal("a nil config must read as absent")
	}
}
