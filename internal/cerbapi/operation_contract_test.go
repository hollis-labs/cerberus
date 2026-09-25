package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/cerberus/internal/audit"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/connector"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Every built-in operation declares the whole contract, and every Definition()
// returns it already finalized, so the derived fields a surface reads are
// the ones the contract implies.
func TestBuiltinDefinitionsDeclareTheContract(t *testing.T) {
	for _, def := range builtinConnectorDefinitions() {
		if err := contract.ValidateDefinition(def); err != nil {
			t.Errorf("%v", err)
		}
		if again := contract.Finalize(def); !reflect.DeepEqual(again, def) {
			t.Errorf("%s: Definition() is not finalized", def.ID)
		}
		for _, op := range def.Operations {
			if want := !op.Effect.ReadOnly() || op.LocalFS == contract.LocalFSWrites; op.RequiresAck != want {
				t.Errorf("%s.%s: effect %s, local_fs %s, but requires_ack %v", def.ID, op.Name, op.Effect, op.LocalFS, op.RequiresAck)
			}
		}
		manifest := contract.ManifestFromDefinition(def)
		if err := manifest.Validate(); err != nil {
			t.Errorf("%s: generated manifest invalid: %v", def.ID, err)
		}
		if gaps := manifest.ContractGaps(); len(gaps) != 0 {
			t.Errorf("%s: generated manifest has gaps: %v", def.ID, gaps)
		}
	}
}

// The classification the PR's UAT table documents. A change here changes
// which surfaces need --ack, so it is pinned rather than left to review.
func TestBuiltinEffectClassification(t *testing.T) {
	want := map[string]contract.Effect{
		"docker.list_containers": contract.EffectRead, "docker.start": contract.EffectLifecycle,
		"docker.stop": contract.EffectLifecycle, "docker.destroy": contract.EffectDestructive,
		"docker.logs":   contract.EffectReadSensitive,
		"github.status": contract.EffectRead, "github.list_releases": contract.EffectRead, "github.list_workflow_runs": contract.EffectRead,
		"ssh.status": contract.EffectRead, "ssh.exec": contract.EffectExec, "ssh.put": contract.EffectWrite,
		"ssh.get": contract.EffectReadSensitive, "ssh.put_dir": contract.EffectWrite, "ssh.get_dir": contract.EffectReadSensitive,
		"ssh.stop": contract.EffectLifecycle,
	}
	// Ops whose ack comes from local_fs: writes rather than their effect.
	localWriteAck := map[string]bool{"ssh.get": true, "ssh.get_dir": true}
	seen := map[string]bool{}
	for _, def := range builtinConnectorDefinitions() {
		for _, op := range def.Operations {
			key := def.ID + "." + op.Name
			seen[key] = true
			if byLocalWrite := op.Effect.ReadOnly() && op.RequiresAck; byLocalWrite != localWriteAck[key] {
				t.Errorf("%s: read effect with ack from local_fs writes = %v, want %v", key, byLocalWrite, localWriteAck[key])
			}
			if got, ok := want[key]; !ok {
				t.Errorf("%s is not classified in this test", key)
			} else if op.Effect != got {
				t.Errorf("%s: effect %s, want %s", key, op.Effect, got)
			}
		}
	}
	for key := range want {
		if !seen[key] {
			t.Errorf("%s is classified here but not declared", key)
		}
	}
}

// The acceptance sweep for argument refusals: for every declared operation,
// a call with a bad argument and no credential is refused as invalid_args,
// never credential_missing, and nothing is resolved. Bad means an undeclared
// key, a missing required key, or a local-only key from a remote surface.
func TestArgumentRefusalsNeverNeedACredential(t *testing.T) {
	resolves := 0
	svc := NewExternalConnectorService(audit.NewMemory(), resolveCountingRegistry(&resolves))
	svc.SetResourceLookup(sshTestLookup())
	socket := WithCallerSurface(context.Background(), SurfaceSocket)

	for _, def := range svc.Definitions() {
		for _, op := range def.Operations {
			cases := map[string]struct {
				ctx    context.Context
				config map[string]any
			}{}
			undeclared := sweepConfig(def.ID, op)
			undeclared["zz_not_declared"] = "x"
			cases["undeclared key"] = struct {
				ctx    context.Context
				config map[string]any
			}{context.Background(), undeclared}
			if hasRequirement(op) {
				cases["missing required"] = struct {
					ctx    context.Context
					config map[string]any
				}{context.Background(), map[string]any{}}
			}
			for _, in := range op.Inputs {
				if in.Scope == contract.InputLocal {
					cfg := sweepConfig(def.ID, op)
					cfg[in.Name] = "x"
					cases["local-only "+in.Name+" over the socket"] = struct {
						ctx    context.Context
						config map[string]any
					}{socket, cfg}
				}
			}
			for name, tc := range cases {
				resolves = 0
				_, err := svc.Execute(tc.ctx, ExternalConnectorOperationArgs{
					Connector: def.ID, Operation: op.Name, Config: tc.config, Acknowledged: true,
				})
				if code := connectorErrorCode(err); code != ExternalConnectorInvalidArgs {
					t.Errorf("%s.%s %s: err = %v, want invalid_args", def.ID, op.Name, name, err)
				}
				if resolves != 0 {
					t.Errorf("%s.%s %s: resolved the connector before refusing", def.ID, op.Name, name)
				}
			}
		}
	}
}

func hasRequirement(op contract.Operation) bool {
	if len(op.OneOf) > 0 {
		return true
	}
	for _, in := range op.Inputs {
		if in.Required {
			return true
		}
	}
	return false
}

// Decision 14 across every declared operation: with valid arguments and no
// acknowledgment, an operation that requires it is refused before
// resolution, and a read goes on to resolve.
func TestAckFollowsTheEffect(t *testing.T) {
	resolves := 0
	svc := NewExternalConnectorService(audit.NewMemory(), resolveCountingRegistry(&resolves))
	svc.SetResourceLookup(sshTestLookup())
	for _, def := range svc.Definitions() {
		for _, op := range def.Operations {
			resolves = 0
			_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
				Connector: def.ID, Operation: op.Name, Config: sweepConfig(def.ID, op),
			})
			switch {
			case op.RequiresAck:
				if code := connectorErrorCode(err); code != ExternalConnectorAckRequired || resolves != 0 {
					t.Errorf("%s.%s (%s): err = %v resolves = %d, want acknowledgment_required before resolution", def.ID, op.Name, op.Effect, err, resolves)
				} else if !strings.Contains(err.Error(), string(op.Effect)+" operation") ||
					(op.Effect.ReadOnly() && !strings.Contains(err.Error(), "writes to the local filesystem")) {
					t.Errorf("%s.%s: refusal does not name the effect: %v", def.ID, op.Name, err)
				}
			case resolves != 1:
				t.Errorf("%s.%s (%s): read did not reach resolution: %v", def.ID, op.Name, op.Effect, err)
			}
		}
	}
}

// A local-only input reaches the connector from the operator's shell and is
// refused by name from the socket and the web console.
func TestLocalOnlyInputsFollowTheSurface(t *testing.T) {
	args := ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers", Config: map[string]any{"host": "ssh://ops@box"}}
	if err := checkFromSurface(SurfaceInProcess, args); err != nil {
		t.Fatalf("in-process: %v", err)
	}
	for _, surface := range []CallerSurface{SurfaceSocket, SurfaceWeb, SurfaceUnknown} {
		err := checkFromSurface(surface, args)
		if code := connectorErrorCode(err); code != ExternalConnectorInvalidArgs || !strings.Contains(err.Error(), "refusing fields (host)") {
			t.Fatalf("%s: err = %v, want host refused", surface, err)
		}
	}
	// Fail closed (I2): a caller that reaches the service without marking
	// its context is remote, never the operator's shell.
	if CallerSurfaceFrom(context.Background()) != SurfaceUnknown {
		t.Fatal("an unmarked context must read as unknown")
	}
	registry := connector.NewRegistry()
	registry.RegisterDefinition(dockerconn.Definition())
	_, err := NewExternalConnectorService(audit.NewMemory(), registry).declaredOperation(context.Background(), args)
	if code := connectorErrorCode(err); code != ExternalConnectorInvalidArgs || !strings.Contains(err.Error(), "refusing fields (host)") {
		t.Fatalf("unmarked context: err = %v, want host refused", err)
	}
}

// The socket server marks every request it serves, so the service behind it
// sees a remote caller.
func TestSocketMarksItsRequests(t *testing.T) {
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(&fakeDockerBackend{}))
	sock := startConnectorSocket(t, NewInProcessClient(WithExternalConnectorService(NewExternalConnectorService(audit.NewMemory(), registry))))
	_, err := sock.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "list_containers", Config: map[string]any{"context": "prod"},
	})
	if code := connectorErrorCode(err); code != ExternalConnectorInvalidArgs || !strings.Contains(err.Error(), "refusing fields (context)") {
		t.Fatalf("err = %v, want the local-only context refused over the socket", err)
	}
}

// Every new refusal text survives redaction intact, recovery included.
func TestContractRefusalsSurviveRedaction(t *testing.T) {
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(&fakeDockerBackend{}))
	svc := NewExternalConnectorService(audit.NewMemory(), registry)
	socket := WithCallerSurface(context.Background(), SurfaceSocket)
	var errs []error
	for _, args := range []ExternalConnectorOperationArgs{
		{Connector: "docker", Operation: "start", Config: map[string]any{"compose_file": "/tmp/x.yml", "token": "t", "password": "p", "api_key": "k"}},
		{Connector: "docker", Operation: "start", Config: map[string]any{}},
		{Connector: "docker", Operation: "start", Config: map[string]any{"container": "web"}},
		{Connector: "ssh", Operation: "exec", Config: map[string]any{"key_file": "/Users/me/.ssh/id_ed25519"}},
	} {
		_, err := svc.Execute(socket, args)
		errs = append(errs, err)
	}
	errs = append(errs, operationFailure(ExternalConnectorOperationArgs{Connector: "docker", Operation: "logs"}, errors.New("exit status 1: reload the daemon")))
	for _, err := range errs {
		var connErr *ExternalConnectorError
		if !errors.As(err, &connErr) {
			t.Fatalf("not a connector error: %v", err)
		}
		raw := connErr.Connector + " " + connErr.Operation + ": " + string(connErr.Code) + ": " + connErr.Err.Error()
		if got := err.Error(); got != raw || redact.Text(raw) != raw {
			t.Errorf("redaction changed the refusal:\n got %q\nwant %q", got, raw)
		}
	}
}

// An uncoded failure past the gates is operation_failed; a coded one keeps
// its code.
func TestOperationFailureCodesOnlyUncodedErrors(t *testing.T) {
	args := ExternalConnectorOperationArgs{Connector: "docker", Operation: "logs"}
	if code := connectorErrorCode(operationFailure(args, errors.New("docker ps: exit status 1"))); code != ExternalConnectorOperationFailed {
		t.Fatalf("uncoded: %s", code)
	}
	coded := externalConnectorError(args, ExternalConnectorInvalidArgs, errors.New("bad"))
	if code := connectorErrorCode(operationFailure(args, coded)); code != ExternalConnectorInvalidArgs {
		t.Fatalf("coded error recoded as %s", code)
	}
	if operationFailure(args, nil) != nil {
		t.Fatal("nil became an error")
	}
}

// healthyPluginProcess answers every call with {"ok":true}.
type healthyPluginProcess struct{ calls int }

func (p *healthyPluginProcess) Init(context.Context, pluginhost.SDKInitParams) (pluginhost.SDKInitResult, error) {
	return pluginhost.SDKInitResult{ID: "contextforge", Version: "dev", Protocol: pluginhost.SDKProtocolVersion}, nil
}
func (p *healthyPluginProcess) Load(context.Context) (pluginhost.SDKLoadResult, error) {
	return pluginhost.SDKLoadResult{}, nil
}
func (p *healthyPluginProcess) Unload(context.Context) error { return nil }
func (p *healthyPluginProcess) Health(context.Context) (pluginhost.SDKHealthResult, error) {
	return pluginhost.SDKHealthResult{OK: true}, nil
}
func (p *healthyPluginProcess) CallTool(context.Context, pluginhost.SDKMCPCallRequest) (pluginhost.SDKMCPCallResult, error) {
	p.calls++
	return pluginhost.SDKMCPCallResult{Content: json.RawMessage(`{"ok":true}`)}, nil
}
func (p *healthyPluginProcess) Close() error { return nil }

// contextforgeFixture is a managed plugin lane with a ContextForge-shaped
// plugin: get_health, with the effect given (or none), and nothing else.
func contextforgeFixture(t *testing.T, effect contract.Effect) (*ManagedPluginConnectorService, *healthyPluginProcess) {
	t.Helper()
	svc, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", io.Discard, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewManagedPluginConnectorService: %v", err)
	}
	process := &healthyPluginProcess{}
	svc.manager = pluginhost.NewManager(nil, echoingLauncher{process: process}, "test")
	svc.manager.RegisterInstalled(pluginhost.InstalledPlugin{
		ID:     "contextforge",
		Origin: pluginhost.OriginInstalled,
		Manifest: contract.Manifest{
			APIVersion: contract.ManifestAPIVersion, Kind: "Connector", ID: "contextforge", Version: "dev",
			ResourceTypes: []string{"gateway"},
			Operations: []contract.ManifestOperation{
				{Name: "get_health", Effect: effect, InputSchema: contract.ObjectSchema(map[string]any{})},
			},
		},
	})
	if err := svc.manager.Load(context.Background(), "contextforge"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return svc, process
}

// ContextForge's get_health is how an operator tells a down tunnel from a
// down gateway, so it must keep working unacknowledged once its effect is
// declared read — on both routes to a plugin. Until it is declared, it is a
// gap: exec, acknowledgment required, and reported.
func TestContextForgeHealthNeedsNoAckOnceDeclaredRead(t *testing.T) {
	routes := map[string]func(*ManagedPluginConnectorService) error{
		"admin lane": func(svc *ManagedPluginConnectorService) error {
			_, err := NewExternalConnectorService(audit.NewMemory(), connector.NewRegistry(), svc).Execute(context.Background(),
				ExternalConnectorOperationArgs{Connector: "contextforge", Operation: "get_health"})
			return err
		},
		"managed exec": func(svc *ManagedPluginConnectorService) error {
			_, err := svc.Execute(context.Background(), "contextforge", PluginConnectorExecArgs{Operation: "get_health"})
			return err
		},
	}
	for name, run := range routes {
		t.Run(name+"/declared read", func(t *testing.T) {
			svc, process := contextforgeFixture(t, contract.EffectRead)
			if err := run(svc); err != nil {
				t.Fatalf("get_health: %v", err)
			}
			if process.calls != 1 {
				t.Fatalf("plugin called %d times, want 1", process.calls)
			}
			assertGaps(t, svc, 0)
		})
		t.Run(name+"/undeclared", func(t *testing.T) {
			svc, process := contextforgeFixture(t, "")
			err := run(svc)
			if code := connectorErrorCode(err); code != ExternalConnectorAckRequired {
				t.Fatalf("err = %v, want acknowledgment_required for a gap", err)
			}
			if process.calls != 0 {
				t.Fatal("plugin called for an unacknowledged gap")
			}
			assertGaps(t, svc, 1)
		})
	}
}

// assertGaps checks managed list reports n contract gaps, with the field
// present even when empty.
func assertGaps(t *testing.T, svc *ManagedPluginConnectorService, n int) {
	t.Helper()
	states, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, state := range states {
		if len(state.ContractGaps) != n {
			t.Fatalf("contract_gaps = %v, want %d", state.ContractGaps, n)
		}
		data, err := redact.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"contract_gaps":`) {
			t.Fatalf("contract_gaps missing from %s", data)
		}
		for _, gap := range state.ContractGaps {
			if !strings.Contains(string(data), "treated as exec") {
				t.Fatalf("gap text redacted: %s (want %q)", data, gap)
			}
		}
	}
}
