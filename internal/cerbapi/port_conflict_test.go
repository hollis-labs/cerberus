package cerbapi

import (
	"context"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
)

func TestPortConflictRefusesActivationBeforeBuildOrRuntimeMutation(t *testing.T) {
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{
		{ID: "first", Project: "one", Type: "process", Connector: "local", Config: map[string]any{"port": 5173, "command": []string{"/bin/false"}, "build_strategy": map[string]any{"kind": "must-not-build"}}},
		{ID: "second", Project: "two", Type: "process", Connector: "local", Config: map[string]any{"port": 5173}},
	}}
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigV2(cfg))
	operations := []func(context.Context, string, ...MutationOption) (*OpResult, error){svc.ApplyResource, svc.ReloadResource, svc.DeployResource}
	for _, operation := range operations {
		result, err := operation(context.Background(), "first", WithAcknowledged(true))
		if err != nil || result.Success || !strings.Contains(result.Error, "TCP port 5173") || !strings.Contains(result.Error, "second") {
			t.Fatalf("unsafe activation accepted: %+v %v", result, err)
		}
	}
}
