package pluginhost

import (
	"encoding/json"
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func TestMCPRequestFromOperation(t *testing.T) {
	req := MCPRequestFromOperation(OperationArgs{
		Connector: "docker",
		Operation: "logs",
		Config:    map[string]any{"container": "web", "lines": 10},
		DryRun:    true,
	})

	if req.ToolName != "cerberus_docker_logs" {
		t.Fatalf("ToolName = %q, want cerberus_docker_logs", req.ToolName)
	}
	if req.Arguments["container"] != "web" || req.Arguments["lines"] != 10 || req.Arguments["dry_run"] != true {
		t.Fatalf("Arguments = %#v", req.Arguments)
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
}

func TestOperationResultFromMCPJSONContent(t *testing.T) {
	content, err := json.Marshal(map[string]any{"state": "running"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := OperationResultFromMCP(
		OperationArgs{Connector: "docker", Operation: "status"},
		SDKMCPCallResult{Content: content},
	)
	if err != nil {
		t.Fatalf("OperationResultFromMCP: %v", err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data = %T, want map", result.Data)
	}
	if data["state"] != "running" {
		t.Fatalf("state = %#v, want running", data["state"])
	}
}

func TestOperationResultFromMCPError(t *testing.T) {
	_, err := OperationResultFromMCP(
		OperationArgs{Connector: "github", Operation: "status"},
		SDKMCPCallResult{Content: []byte(`"missing token"`), IsError: true},
	)
	if err == nil {
		t.Fatal("expected plugin tool error")
	}
}
