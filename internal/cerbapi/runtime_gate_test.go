package cerbapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/pipeline"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The runtime contracts are complete and finalized, like every built-in's.
func TestRuntimeDefinitionsDeclareTheContract(t *testing.T) {
	for _, def := range RuntimeDefinitions() {
		if err := contract.ValidateDefinition(def); err != nil {
			t.Errorf("%v", err)
		}
		if again := contract.Finalize(def); !reflect.DeepEqual(again, def) {
			t.Errorf("%s: Definition() is not finalized", def.ID)
		}
	}
}

// Decisions 11 and 14, pinned: every resource mutation and pipeline run
// needs acknowledgment, with these effects.
func TestRuntimeEffectClassification(t *testing.T) {
	want := map[string]contract.Effect{
		"local.deploy": contract.EffectLifecycle, "local.apply": contract.EffectLifecycle,
		"local.reload": contract.EffectLifecycle, "local.stop": contract.EffectLifecycle,
		"local.sync": contract.EffectWrite, "local.remove": contract.EffectDestructive,
		"local.ensure_fresh": contract.EffectLifecycle,
		"pipeline.run":       contract.EffectExec, "infra.run_profile": contract.EffectExec,
		// The reads the MCP tools serve. They change nothing and are not gated.
		"local.list": contract.EffectRead, "local.status": contract.EffectRead, "local.inspect": contract.EffectRead,
		"local.doctor": contract.EffectRead, "local.logs": contract.EffectReadSensitive,
		"pipeline.list":   contract.EffectRead,
		"cerberus.health": contract.EffectRead, "cerberus.project_list": contract.EffectRead,
		"cerberus.connector_list": contract.EffectRead, "cerberus.connector_describe": contract.EffectRead,
	}
	seen := 0
	for _, def := range RuntimeDefinitions() {
		for _, op := range def.Operations {
			key := def.ID + "." + op.Name
			seen++
			if got, ok := want[key]; !ok || op.Effect != got {
				t.Errorf("%s: effect %s, want %s", key, op.Effect, want[key])
			}
			if op.RequiresAck != !op.Effect.ReadOnly() {
				t.Errorf("%s: requires_ack %v for effect %s", key, op.RequiresAck, op.Effect)
			}
		}
	}
	if seen != len(want) {
		t.Errorf("declared %d runtime operations, classified %d", seen, len(want))
	}
}

type runtimeMutation struct {
	name string
	call func(context.Context, *ResourceRuntimeService, ...MutationOption) error
}

func runtimeMutations(id string) []runtimeMutation {
	wrap := func(f func(*ResourceRuntimeService) func(context.Context, string, ...MutationOption) (*OpResult, error)) func(context.Context, *ResourceRuntimeService, ...MutationOption) error {
		return func(ctx context.Context, s *ResourceRuntimeService, opts ...MutationOption) error {
			_, err := f(s)(ctx, id, opts...)
			return err
		}
	}
	return []runtimeMutation{
		{"deploy", wrap(func(s *ResourceRuntimeService) func(context.Context, string, ...MutationOption) (*OpResult, error) {
			return s.DeployResource
		})},
		{"apply", wrap(func(s *ResourceRuntimeService) func(context.Context, string, ...MutationOption) (*OpResult, error) {
			return s.ApplyResource
		})},
		{"reload", wrap(func(s *ResourceRuntimeService) func(context.Context, string, ...MutationOption) (*OpResult, error) {
			return s.ReloadResource
		})},
		{"stop", wrap(func(s *ResourceRuntimeService) func(context.Context, string, ...MutationOption) (*OpResult, error) {
			return s.StopResource
		})},
		{"sync", wrap(func(s *ResourceRuntimeService) func(context.Context, string, ...MutationOption) (*OpResult, error) {
			return s.SyncResource
		})},
		{"remove", wrap(func(s *ResourceRuntimeService) func(context.Context, string, ...MutationOption) (*OpResult, error) {
			return s.RemoveResource
		})},
		{"pipeline run", func(ctx context.Context, s *ResourceRuntimeService, opts ...MutationOption) error {
			_, err := s.RunPipeline(ctx, id, opts...)
			return err
		}},
	}
}

// The gate runs before anything: with the runtime's operation lock and the
// pipeline lock held, and for an id that names nothing, an unacknowledged
// call returns acknowledgment_required at once — it takes no lock and looks
// nothing up.
func TestRuntimeAckComesBeforeAnything(t *testing.T) {
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigV2(&config.ConfigV2{}))
	svc.opMu.Lock()
	svc.pipelineMu.Lock()
	defer svc.opMu.Unlock()
	defer svc.pipelineMu.Unlock()
	for _, m := range runtimeMutations("nothing-by-this-name") {
		done := make(chan error, 1)
		go func() { done <- m.call(context.Background(), svc) }()
		select {
		case err := <-done:
			var connErr *ExternalConnectorError
			if !errors.As(err, &connErr) || connErr.Code != ExternalConnectorAckRequired {
				t.Errorf("%s: err = %v, want acknowledgment_required", m.name, err)
				continue
			}
			if got := redact.Text(err.Error()); got != err.Error() {
				t.Errorf("%s: redaction changed the refusal:\n got %q\nwant %q", m.name, got, err.Error())
			}
			if !strings.Contains(err.Error(), `"nothing-by-this-name"`) {
				t.Errorf("%s: refusal does not name its target: %v", m.name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: the gate waited on a lock, so it does not run first", m.name)
		}
	}
}

// Acknowledged, the same calls go past the gate and meet the lookup.
func TestRuntimeAckedCallsReachTheService(t *testing.T) {
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigV2(&config.ConfigV2{}))
	for _, m := range runtimeMutations("nothing-by-this-name") {
		err := m.call(context.Background(), svc, WithAcknowledged(true))
		var connErr *ExternalConnectorError
		if errors.As(err, &connErr) && connErr.Code == ExternalConnectorAckRequired {
			t.Errorf("%s: refused although acknowledged", m.name)
		}
	}
}

// The resource key table refuses a key it does not declare, on the same
// terms as a connector operation.
func TestRuntimeGateChecksTheKeyTable(t *testing.T) {
	err := runtimeGate(context.Background(), localconn.Definition(), localconn.OpStop, map[string]any{"id": "x", "force": true}, MutationOpts{Acknowledged: true})
	if code := connectorErrorCode(err); code != ExternalConnectorInvalidArgs || !strings.Contains(err.Error(), "refusing fields (force)") {
		t.Fatalf("err = %v, want force refused", err)
	}
	err = runtimeGate(context.Background(), pipeline.Definition(), "rewind", map[string]any{"id": "x"}, MutationOpts{Acknowledged: true})
	if code := connectorErrorCode(err); code != ExternalConnectorUnsupported {
		t.Fatalf("undeclared op: err = %v, want operation_unsupported", err)
	}
}

// Supervision is not an operation request (Decision 14). The monitor
// restarts a crashed auto_restart workload with no acknowledgment anywhere —
// which the gate would refuse — while an explicit apply on the same runtime,
// unacknowledged, is refused. So the restart path never passes the gate.
func TestMonitorRestartNeverHitsTheGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	marker := filepath.Join(t.TempDir(), "restarted")
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: "crashy", Type: "process", Connector: "local",
		Config: map[string]any{"auto_restart": true, "command": []string{"/usr/bin/touch", marker}}}}}
	runtime := NewResourceRuntimeService(WithResourceRuntimeConfigV2(cfg))

	if _, err := runtime.ApplyResource(context.Background(), "crashy"); connectorErrorCode(err) != ExternalConnectorAckRequired {
		t.Fatalf("explicit apply without ack: err = %v, want the gate to refuse it", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the refused apply started the workload")
	}

	m := NewResourceMonitor(runtime, ResourceMonitorConfig{CheckInterval: time.Hour, DefaultMaxRestartAttempts: 3}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.checkAllResources(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the monitor did not restart the workload: %v", m.lastError)
		}
		time.Sleep(time.Millisecond)
	}
	if m.lastError["crashy"] != "" {
		t.Fatalf("monitor restart reported an error: %s", m.lastError["crashy"])
	}
}

// EnsureFresh never acknowledges on its own: the caller's acknowledgment
// reaches the mutation it chooses, and without one that mutation is refused.
func TestEnsureFreshCarriesTheCallersAck(t *testing.T) {
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigV2(&config.ConfigV2{}))
	if _, err := EnsureFresh(context.Background(), svc, "anything", true); connectorErrorCode(err) != ExternalConnectorAckRequired {
		t.Fatalf("unacknowledged ensure-fresh: err = %v, want acknowledgment_required", err)
	}
	if _, err := EnsureFresh(context.Background(), svc, "anything", true, WithAcknowledged(true)); connectorErrorCode(err) == ExternalConnectorAckRequired {
		t.Fatalf("acknowledged ensure-fresh was refused: %v", err)
	}
}

// Over the socket the acknowledgment travels in the body, and a refusal keeps
// its code and the 409 the status table gives it.
func TestResourceAckTravelsTheSocket(t *testing.T) {
	sock := startConnectorSocket(t, NewInProcessClient(WithResourceRuntimeService(NewResourceRuntimeService(WithResourceRuntimeConfigV2(&config.ConfigV2{})))))
	_, err := sock.StopResource(context.Background(), "svc")
	if code := connectorErrorCode(err); code != ExternalConnectorAckRequired {
		t.Fatalf("unacknowledged stop over the socket: err = %v", err)
	}
	if _, err := sock.StopResource(context.Background(), "svc", WithAcknowledged(true)); connectorErrorCode(err) == ExternalConnectorAckRequired {
		t.Fatalf("acknowledgment lost on the socket: %v", err)
	}
	if _, err := sock.RunPipeline(context.Background(), "p"); connectorErrorCode(err) != ExternalConnectorAckRequired {
		t.Fatalf("unacknowledged pipeline run over the socket: err = %v", err)
	}
	status, _ := postSocket(t, sock, "/resources/svc/remove")
	if status != 409 {
		t.Fatalf("socket status %d, want 409", status)
	}
}
