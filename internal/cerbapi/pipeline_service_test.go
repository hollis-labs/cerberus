package cerbapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/domain"
)

func TestPipelineTransportUsesSharedRuntimeAndFreshConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig := func(name, command string) {
		t.Helper()
		body := "version: 2\npipelines:\n  - id: check\n    name: " + name + "\n    stages:\n      - name: verify\n        actions:\n          - type: shell\n            command: " + command + "\n"
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("Original", "'true'")
	runtime := NewResourceRuntimeService(WithResourceRuntimeConfigPath(path))
	// The client has no config of its own: all pipeline operations must delegate.
	client := startConnectorSocket(t, NewInProcessClient(WithResourceRuntimeService(runtime)))
	list, err := client.ListPipelines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Name != "Original" {
		t.Fatalf("list = %#v", list)
	}
	writeConfig("Updated", "'exit 7'")
	detail, err := client.GetPipeline(context.Background(), "check")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Definition.Name != "Updated" || detail.ValidationError != "" {
		t.Fatalf("detail = %#v", detail)
	}
	result, err := client.RunPipeline(context.Background(), "check")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal(result.Error)
	}
	execution, err := result.Execution()
	if err != nil {
		t.Fatal(err)
	}
	if execution.Status != domain.StateFailed || !strings.Contains(execution.Error, "exit status 7") {
		t.Fatalf("execution = %#v", execution)
	}
	missing, err := client.GetPipeline(context.Background(), "missing")
	if err != nil || missing != nil {
		t.Fatalf("missing = %#v, %v", missing, err)
	}
}

func TestPipelineDetailRetainsInvalidDefinition(t *testing.T) {
	runtime := NewResourceRuntimeService(WithResourceRuntimeConfigV2(&config.ConfigV2{
		Pipelines: []config.PipelineDef{{ID: "invalid", Name: "Inspectable"}},
	}))
	client := startConnectorSocket(t, NewInProcessClient(WithResourceRuntimeService(runtime)))
	detail, err := client.GetPipeline(context.Background(), "invalid")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Definition.Name != "Inspectable" || detail.ValidationError != `resolve pipeline: pipeline "invalid" has no stages` {
		t.Fatalf("detail = %#v", detail)
	}
	result, err := client.RunPipeline(context.Background(), "invalid")
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.Error != detail.ValidationError {
		t.Fatalf("run = %#v", result)
	}
}
