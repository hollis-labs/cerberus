package pluginhost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func loadedGateManager(t *testing.T, ops ...contract.ManifestOperation) (*Manager, *fakeProcess) {
	t.Helper()
	plugin := validInstalledPlugin()
	plugin.Manifest.Operations = ops
	process := &fakeProcess{
		initResult: SDKInitResult{ID: plugin.ID, Version: plugin.Version, Protocol: SDKProtocolVersion},
		callResult: SDKMCPCallResult{Content: []byte(`{"ok":true}`)},
	}
	manager := NewManager(nil, fakeLauncher{process: process}, "test")
	manager.RegisterInstalled(plugin)
	if err := manager.Load(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return manager, process
}

// A manifest must not be able to opt a destructive operation out of the
// host's acknowledgment gate by leaving requires_ack unset.
func TestDestructiveOperationNeedsAckWhateverRequiresAckSays(t *testing.T) {
	manager, process := loadedGateManager(t, contract.ManifestOperation{
		Name: "destroy", Destructive: true, RequiresAck: false, InputSchema: contract.ObjectSchema(map[string]any{}),
	})
	_, err := manager.ExecuteOperation(context.Background(), OperationArgs{Connector: "docker", Operation: "destroy"})
	if err == nil || !strings.Contains(err.Error(), "requires operator acknowledgment") {
		t.Fatalf("err = %v, want acknowledgment refusal", err)
	}
	if process.calls != 0 {
		t.Fatal("plugin was called without acknowledgment")
	}

	if _, err := manager.ExecuteOperation(context.Background(), OperationArgs{Connector: "docker", Operation: "destroy", Acknowledged: true}); err != nil {
		t.Fatalf("acknowledged destroy: %v", err)
	}
	if process.calls != 1 {
		t.Fatalf("calls = %d, want 1", process.calls)
	}
}

func TestOperationAllowedIgnoresRequiresAck(t *testing.T) {
	for _, tier := range []InstallOrigin{OriginInstalled, OriginInstalled, OriginInstalled} {
		op := contract.ManifestOperation{Name: "wipe", Destructive: true, RequiresAck: false}
		if err := OperationAllowed(tier, op, false); err == nil {
			t.Errorf("%s: destructive op without ack allowed", tier)
		}
		if err := OperationAllowed(tier, op, true); err != nil {
			t.Errorf("%s: acknowledged destructive op refused: %v", tier, err)
		}
	}
}

// A dry run reaches the plugin only when the manifest declares supports_dry.
func TestDryRunNeverCallsPluginWithoutSupportsDry(t *testing.T) {
	manager, process := loadedGateManager(t,
		contract.ManifestOperation{Name: "restart", InputSchema: contract.ObjectSchema(map[string]any{})},
		contract.ManifestOperation{Name: "destroy", Destructive: true, InputSchema: contract.ObjectSchema(map[string]any{})},
		contract.ManifestOperation{Name: "scale", Effect: contract.EffectLifecycle, SupportsDry: true, InputSchema: contract.ObjectSchema(map[string]any{})},
	)
	for _, op := range []string{"restart", "destroy"} {
		_, err := manager.ExecuteOperation(context.Background(), OperationArgs{Connector: "docker", Operation: op, DryRun: true, Acknowledged: true})
		if !errors.Is(err, ErrPreviewUnsupported) {
			t.Fatalf("%s dry run err = %v, want ErrPreviewUnsupported", op, err)
		}
	}
	if process.calls != 0 {
		t.Fatalf("plugin called %d times for dry runs it cannot preview", process.calls)
	}

	if _, err := manager.ExecuteOperation(context.Background(), OperationArgs{Connector: "docker", Operation: "scale", DryRun: true, Acknowledged: true}); err != nil {
		t.Fatalf("supports_dry dry run: %v", err)
	}
	if process.calls != 1 {
		t.Fatalf("calls = %d, want 1 forwarded dry run", process.calls)
	}
}

func TestPreviewUnsupportedSurvivesRedaction(t *testing.T) {
	msg := ErrPreviewUnsupported.Error()
	if got := redact.Text(msg); got != msg {
		t.Fatalf("redact.Text changed ErrPreviewUnsupported:\n got %q\nwant %q", got, msg)
	}
}
