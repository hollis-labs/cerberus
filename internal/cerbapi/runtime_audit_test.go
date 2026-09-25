package cerbapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/connector"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// telemetryProcess answers every call with a result carrying telemetry, one
// event of which echoes the credential the plugin was given.
type telemetryProcess struct{ healthyPluginProcess }

func (p *telemetryProcess) CallTool(context.Context, pluginhost.SDKMCPCallRequest) (pluginhost.SDKMCPCallResult, error) {
	return pluginhost.SDKMCPCallResult{Content: json.RawMessage(`{"items":[1],"cerberus_telemetry":[` +
		`{"kind":"step","message":"listed with ` + resolvedSentinel + `","target":"things"},` +
		`{"kind":"warning","message":"one item skipped"}]}`)}, nil
}

func telemetryManagedService(t *testing.T, sink audit.Sink) *ManagedPluginConnectorService {
	t.Helper()
	svc, err := NewManagedPluginConnectorService(sink, "test", io.Discard, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc.manager = pluginhost.NewManager(nil, echoingLauncher{process: &telemetryProcess{}}, "test", pluginhost.WithSecretResolver(sentinelResolver{}))
	svc.manager.RegisterInstalled(pluginhost.InstalledPlugin{
		ID: "leaky", Origin: pluginhost.OriginInstalled,
		Manifest: contract.Manifest{
			APIVersion: contract.ManifestAPIVersion, Kind: "Connector", ID: "leaky", Version: "dev",
			Config: contract.ConfigSchema{Secrets: []contract.SecretRequirement{{Name: "token", Env: "CERBERUS_LEAKY_TOKEN"}}},
			Operations: []contract.ManifestOperation{
				{Name: "list_things", Effect: contract.EffectRead, InputSchema: contract.ObjectSchema(map[string]any{})},
			},
		},
	})
	if err := svc.manager.Load(context.Background(), "leaky"); err != nil {
		t.Fatal(err)
	}
	return svc
}

// What a plugin reports for an operation lands on that operation's outcome
// record, redacted by the plugin's value redactor, and never reaches the
// caller: the host strips it from the result. The intent carries none.
func TestPluginTelemetryEnrichesTheOutcomeOnly(t *testing.T) {
	sink := audit.NewMemory()
	managed := telemetryManagedService(t, sink)
	result, err := NewExternalConnectorService(sink, connector.NewRegistry(), managed).Execute(context.Background(),
		ExternalConnectorOperationArgs{Connector: "leaky", Operation: "list_things"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.Data)
	if strings.Contains(string(data), "cerberus_telemetry") {
		t.Fatalf("telemetry reached the caller: %s", data)
	}
	var outcome, intent audit.Record
	for _, rec := range sink.Records() {
		switch rec.Kind {
		case audit.KindOutcome:
			outcome = rec
		case audit.KindIntent:
			intent = rec
		}
	}
	if intent.PluginTelemetry != nil {
		t.Fatal("the intent carries telemetry; only the host's outcome may")
	}
	tel := outcome.PluginTelemetry
	if tel == nil || len(tel.Events) != 2 || tel.Events[1].Message != "one item skipped" || tel.Events[0].Target != "things" {
		t.Fatalf("telemetry %+v", tel)
	}
	encoded, _ := json.Marshal(outcome)
	if strings.Contains(string(encoded), resolvedSentinel) {
		t.Fatalf("a resolved credential reached the record: %s", encoded)
	}
}

// The resource mutators, a pipeline run and a deploy-profile run are
// recorded like the admin lane: intent before the gate, so a refusal is
// recorded, and outcome after — an OpResult reporting failure is recorded as
// operation_failed.
func TestRuntimeOperationsAreRecorded(t *testing.T) {
	sink := audit.NewMemory()
	runtime := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(&config.ConfigV2{}))
	ctx := WithCallerSurface(context.Background(), SurfaceInProcess)

	_, _ = runtime.StopResource(ctx, "svc")
	_, _ = runtime.DeployResource(ctx, "svc", WithAcknowledged(true))
	_, _ = runtime.RunPipeline(ctx, "p", WithAcknowledged(true))

	got := pairs(t, sink.Records())
	if len(got) != 3 {
		t.Fatalf("%d operations recorded, want 3", len(got))
	}
	want := map[string][2]string{
		"stop":   {audit.DecisionRefused, "acknowledgment_required"},
		"deploy": {audit.DecisionAllowed, "operation_failed"},
		"run":    {audit.DecisionAllowed, "operation_failed"},
	}
	for _, p := range got {
		outcome := p[1]
		w := want[outcome.Operation]
		if outcome.Decision != w[0] || outcome.OutcomeCode != w[1] || outcome.Principal.Surface != "in_process" {
			t.Errorf("%s: %+v, want %v", outcome.Operation, outcome, w)
		}
		if outcome.Target.Fields["id"] == "" {
			t.Errorf("%s: target not recorded: %+v", outcome.Operation, outcome.Target)
		}
	}
}

func TestDeployProfileRunIsRecorded(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".vercel"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".vercel", "project.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := audit.NewMemory()
	profile := infra.DeploymentProfile{ID: "site", Provider: "vercel", RepoPath: repo, DeployCommand: "true"}
	if _, err := RunDeploymentProfile(context.Background(), sink, nil, profile); connectorErrorCode(err) != ExternalConnectorAckRequired {
		t.Fatalf("unacknowledged run: %v", err)
	}
	if _, err := RunDeploymentProfile(context.Background(), sink, nil, profile, WithAcknowledged(true)); err != nil {
		t.Fatal(err)
	}
	got := pairs(t, sink.Records())
	if len(got) != 2 {
		t.Fatalf("%d runs recorded, want 2", len(got))
	}
	for _, p := range got {
		if p[1].Decision == audit.DecisionAllowed && strings.Join(p[1].CredentialNames, ",") != "vercel/scope,vercel/token" {
			t.Fatalf("an allowed run does not name its credentials: %+v", p[1])
		}
	}
}

// Decision 8 on the supervision lane: an unwritable log refuses a mutation
// before it takes the operation lock, and a pipeline run likewise.
func TestUnwritableAuditRefusesRuntimeMutations(t *testing.T) {
	runtime := NewResourceRuntimeService(audit.Failing{}, WithResourceRuntimeConfigV2(&config.ConfigV2{}))
	runtime.opMu.Lock()
	defer runtime.opMu.Unlock()
	done := make(chan error, 2)
	go func() {
		_, err := runtime.StopResource(context.Background(), "svc", WithAcknowledged(true))
		done <- err
	}()
	go func() { _, err := runtime.RunPipeline(context.Background(), "p", WithAcknowledged(true)); done <- err }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if code := connectorErrorCode(err); code != ExternalConnectorAuditUnavailable {
				t.Fatalf("err = %v, want audit_unavailable", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the refusal waited on the operation lock")
		}
	}
}

// The monitor's restarts are recorded as an automation principal with the
// reason, and are never gated — even an unwritable log does not stop one.
func TestMonitorRestartIsRecordedAsAutomation(t *testing.T) {
	for name, sink := range map[string]audit.Sink{"recorded": audit.NewMemory(), "unwritable": audit.Failing{}} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			marker := filepath.Join(t.TempDir(), "restarted")
			cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: "crashy", Type: "process", Connector: "local",
				Config: map[string]any{"auto_restart": true, "command": []string{"/usr/bin/touch", marker}}}}}
			runtime := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(cfg))
			m := NewResourceMonitor(runtime, ResourceMonitorConfig{CheckInterval: time.Hour, DefaultMaxRestartAttempts: 3}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			m.checkAllResources(context.Background())
			deadline := time.Now().Add(2 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the monitor did not restart the workload")
				}
				time.Sleep(time.Millisecond)
			}
			mem, ok := sink.(*audit.Memory)
			if !ok {
				return
			}
			got := pairs(t, mem.Records())
			if len(got) != 1 {
				t.Fatalf("%d records pairs, want 1", len(got))
			}
			for _, p := range got {
				intent := p[0]
				if intent.Principal.Kind != audit.PrincipalAutomation || intent.Principal.Surface != "monitor" || intent.Principal.SelfReported {
					t.Fatalf("principal %+v", intent.Principal)
				}
				if !strings.Contains(intent.Reason, "auto_restart") || !strings.Contains(intent.Reason, "attempt 1 of 3") {
					t.Fatalf("reason %q", intent.Reason)
				}
				if intent.Acknowledged {
					t.Fatal("automation recorded as acknowledged")
				}
			}
		})
	}
}
