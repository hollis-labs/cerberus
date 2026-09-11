package cerbapi

import (
	"context"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/daemon"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/pipeline/actions"
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
			runtime := NewResourceRuntimeService(WithResourceRuntimeConfigV2(&config.ConfigV2{Resources: []config.ResourceDef{{ID: identity.id, Type: "process", Connector: "local", Config: identity.config}}}))
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
			operations := []func(context.Context, string) (*OpResult, error){runtime.ApplyResource, runtime.SyncResource, runtime.ReloadResource, runtime.StopResource, runtime.RemoveResource,
				func(ctx context.Context, id string) (*OpResult, error) { return runtime.DeployResource(ctx, id) },
			}
			for _, operation := range operations {
				result, err := operation(context.Background(), identity.id)
				if err != nil || result == nil || result.Success || !strings.Contains(result.Error, "refusing to mutate serving Cerberus") || !strings.Contains(result.Error, "kickstart") {
					t.Fatalf("expected actionable self-mutation refusal, got %+v / %v", result, err)
				}
			}
			result, err := EnsureFresh(context.Background(), runtime, identity.id, true)
			if err != nil || result.Success || !strings.Contains(result.Message, "refusing to mutate") {
				t.Fatalf("ensure-fresh bypassed guard: %+v / %v", result, err)
			}
		})
	}
}
