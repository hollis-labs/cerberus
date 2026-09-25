package cerbapi

import (
	"context"
	"github.com/hollis-labs/cerberus/internal/audit"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/daemon"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/pipeline/actions"
)

func TestServingDaemonRefusesMutationBeforeBuildOrInstall(t *testing.T) {
	for _, identity := range []struct {
		name   string
		id     string
		config map[string]any
	}{
		{"resource ID", daemon.CanonicalDaemonResourceID, map[string]any{}},
		{"service label", "alias", map[string]any{"mode": "os_service", "service_name": daemon.CanonicalDaemonServiceLabel}},
		{"artifact alias", "alias", map[string]any{"mode": "os_service", "run_from": "artifact", "artifact_path": "/serving/cerberus"}},
	} {
		t.Run(identity.name, func(t *testing.T) {
			identity.config["build_strategy"] = map[string]any{"kind": "must-not-build"}
			runtime := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(&config.ConfigV2{Resources: []config.ResourceDef{{ID: identity.id, Type: "process", Connector: "local", Config: identity.config}}}))
			runtime.ProtectServingDaemon("/serving/cerberus", daemon.CanonicalDaemonServiceLabel)
			resource := &domain.Resource{ID: identity.id, Config: identity.config}
			spec, specErr := localconn.SpecFromResourceConfig(identity.config)
			if specErr != nil {
				t.Fatal(specErr)
			}
			pipeline := actions.NewDeploy(identity.id, resource, spec, runtime.localConnector())
			if err := pipeline.Execute(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "refusing to mutate") {
				t.Fatalf("pipeline bypassed self-protection: %v", err)
			}
			// Acknowledged, so the contract gate passes and the guard, which
			// is unchanged, is what refuses.
			operations := []func(context.Context, string, ...MutationOption) (*OpResult, error){runtime.ApplyResource, runtime.SyncResource, runtime.ReloadResource, runtime.StopResource, runtime.RemoveResource, runtime.DeployResource}
			for _, operation := range operations {
				result, err := operation(context.Background(), identity.id, WithAcknowledged(true))
				if err != nil || result == nil || result.Success || !strings.Contains(result.Error, "refusing to mutate serving Cerberus") || !strings.Contains(result.Error, "kickstart") {
					t.Fatalf("expected actionable self-mutation refusal, got %+v / %v", result, err)
				}
			}
			result, err := EnsureFresh(context.Background(), runtime, identity.id, true, WithAcknowledged(true))
			if err != nil || result.Success || !strings.Contains(result.Message, "refusing to mutate") {
				t.Fatalf("ensure-fresh bypassed guard: %+v / %v", result, err)
			}
		})
	}
}
