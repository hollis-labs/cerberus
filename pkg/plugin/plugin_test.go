package plugin

import (
	"testing"

	contract "github.com/chrispian/cerberus/pkg/connector"
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
