package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/connector"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	pluginsdk "github.com/hollis-labs/cerberus/pkg/plugin"
	shared "github.com/hollis-labs/libs/security/secrets"
)

type issuedProcess struct {
	callStarted          chan struct{}
	values               map[string]string
	initError, callError bool
	closed               bool
}

func (p *issuedProcess) Init(ctx context.Context, params pluginhost.SDKInitParams) (pluginhost.SDKInitResult, error) {
	p.values = params.Config
	if v := p.values[pluginsdk.IssuedValuePrefix+"runtime_password"]; v != "" {
		i, err := pluginsdk.IssuanceFromConfig(p.values)
		if err != nil {
			return pluginhost.SDKInitResult{}, err
		}
		r, err := shared.New().Resolve(ctx, i.References["runtime_password"])
		if err != nil || r.Secret.Reveal() != v {
			return pluginhost.SDKInitResult{}, errors.New("secret not persisted before Init")
		}
		if p.initError {
			return pluginhost.SDKInitResult{}, errors.New("init echoed " + v)
		}
	}
	return pluginhost.SDKInitResult{ID: "issuer", Version: "dev", Protocol: pluginhost.SDKProtocolVersion, CapabilityContract: 1}, nil
}
func (p *issuedProcess) Load(context.Context) (pluginhost.SDKLoadResult, error) {
	return pluginhost.SDKLoadResult{}, nil
}
func (p *issuedProcess) Unload(context.Context) error { return nil }
func (p *issuedProcess) Health(context.Context) (pluginhost.SDKHealthResult, error) {
	return pluginhost.SDKHealthResult{OK: true}, nil
}
func (p *issuedProcess) Close() error { p.closed = true; return nil }
func (p *issuedProcess) CallTool(ctx context.Context, req pluginhost.SDKMCPCallRequest) (pluginhost.SDKMCPCallResult, error) {
	v := p.values[pluginsdk.IssuedValuePrefix+"runtime_password"]
	args, _ := json.Marshal(req.Arguments)
	if v != "" && strings.Contains(string(args), v) {
		return pluginhost.SDKMCPCallResult{}, errors.New("secret reached operation arguments")
	}
	if p.callStarted != nil && v != "" {
		close(p.callStarted)
		<-ctx.Done()
		return pluginhost.SDKMCPCallResult{}, ctx.Err()
	}
	if p.callError && v != "" {
		return pluginhost.SDKMCPCallResult{}, context.DeadlineExceeded
	}
	data, _ := json.Marshal(map[string]string{"echo": "provider echoed " + v})
	return pluginhost.SDKMCPCallResult{Content: data}, nil
}

type issuedLauncher struct {
	callStarted          chan struct{}
	processes            []*issuedProcess
	initError, callError bool
}

func (l *issuedLauncher) Launch(context.Context, pluginhost.InstalledPlugin) (pluginhost.Process, error) {
	p := &issuedProcess{initError: l.initError, callError: l.callError, callStarted: l.callStarted}
	l.processes = append(l.processes, p)
	return p, nil
}
func issuanceService(t *testing.T, sink audit.Sink) (*ManagedPluginConnectorService, *issuedLauncher, string) {
	t.Helper()
	root := t.TempDir()
	s, err := NewManagedPluginConnectorService(sink, "test", io.Discard, filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	l := &issuedLauncher{}
	s.manager = newTestPluginManager(t, nil, l, "test")
	manifest := contract.Manifest{APIVersion: contract.ManifestAPIVersion, Kind: "Connector", ID: "issuer", Version: "dev", ResourceTypes: []string{"server"}, Operations: []contract.ManifestOperation{{Name: "provision", Effect: contract.EffectWrite, Preview: contract.PreviewPlugin, LocalFS: contract.LocalFSWrites, Output: contract.OutputStructured, Target: contract.TargetDescriptor{Kind: "issuer.cluster", From: []string{"cluster"}}, InputSchema: contract.ObjectSchema(map[string]any{"issuance_id": contract.StringSchema("ID"), "cluster": contract.StringSchema("Cluster")}, "issuance_id", "cluster"), IssuedSecrets: []string{"runtime_password"}}}}
	s.manager.RegisterInstalled(pluginhost.InstalledPlugin{ID: "issuer", Version: "dev", Origin: pluginhost.OriginInstalled, Path: t.TempDir(), Manifest: manifest, Spec: pluginhost.PluginYAML{Capabilities: []pluginhost.CapabilityRequest{{Name: pluginsdk.CredentialIssuanceCapability}}}})
	if err = s.manager.Load(context.Background(), "issuer"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.manager.Unload(context.Background(), "issuer") })
	return s, l, filepath.Join(root, "credential-issuance")
}
func issueArgs() ExternalConnectorOperationArgs {
	return ExternalConnectorOperationArgs{Connector: "issuer", Operation: "provision", Acknowledged: true, Config: map[string]any{"issuance_id": strings.Repeat("a", 32), "cluster": "scratch"}}
}

func TestIssuedCredentialsPersistPrivatelyAndRenderSafelyThroughSocket(t *testing.T) {
	sink := audit.NewMemory()
	s, l, _ := issuanceService(t, sink)
	ext := NewExternalConnectorService(sink, connector.NewRegistry(), s)
	rec := httptest.NewRecorder()
	w, r := BeginHTTPRequest(rec, httptest.NewRequest(http.MethodPost, "/connectors/issuer/operations/provision", nil), SurfaceSocket)
	out, err := ext.Execute(r.Context(), issueArgs())
	if err != nil {
		t.Fatal(err)
	}
	if out.CredentialIssuance == nil || out.CredentialIssuance.State != "completed" {
		t.Fatal("issuance receipt missing")
	}
	if len(l.processes) != 2 || !l.processes[1].closed || l.processes[0].closed {
		t.Fatal("issued credentials must use a disposable process")
	}
	secret := l.processes[1].values[pluginsdk.IssuedValuePrefix+"runtime_password"]
	if secret == "" || l.processes[0].values[pluginsdk.IssuedValuePrefix+"runtime_password"] != "" {
		t.Fatal("issued credential crossed instance boundary")
	}
	writeJSON(w, http.StatusOK, out)
	if strings.Contains(rec.Body.String(), secret) || !strings.Contains(rec.Body.String(), redact.Marker) {
		t.Fatal("issued secret escaped response rendering")
	}
	var rendered ExternalConnectorOperationResult
	if err = json.Unmarshal(rec.Body.Bytes(), &rendered); err != nil {
		t.Fatal(err)
	}
	if rendered.CredentialIssuance == nil || rendered.CredentialIssuance.ID != issueArgs().Config["issuance_id"] || rendered.CredentialIssuance.State != "completed" {
		t.Fatal("receipt metadata lost in wire rendering")
	}
	if !strings.Contains(rec.Body.String(), "file://") {
		t.Fatal("reference was redacted")
	}
	auditJSON, _ := json.Marshal(sink.Records())
	if strings.Contains(string(auditJSON), secret) {
		t.Fatal("issued secret entered audit")
	}
	if _, err = ext.Execute(r.Context(), issueArgs()); err == nil {
		t.Fatal("issuance replay accepted")
	}
	if len(l.processes) != 2 {
		t.Fatal("replay spawned a plugin")
	}
}
func TestIssuanceGatesAndPreviewsDoNotCreateSecrets(t *testing.T) {
	for _, mode := range []string{"ack", "audit", "deny", "approval", "dry_run", "plan"} {
		t.Run(mode, func(t *testing.T) {
			var sink audit.Sink = audit.NewMemory()
			if mode == "audit" {
				sink = audit.Failing{}
			}
			s, l, root := issuanceService(t, sink)
			if mode == "deny" || mode == "approval" {
				decision := policy.Deny
				if mode == "approval" {
					decision = policy.Approve
				}
				withPDP(t, constantPDP{decision: decision})
				withEnforcement(t, nil)
			}
			ext := NewExternalConnectorService(sink, connector.NewRegistry(), s)
			args := issueArgs()
			if mode == "ack" {
				args.Acknowledged = false
			}
			if mode == "dry_run" {
				args.DryRun = true
			}
			if mode == "plan" {
				args.Plan = true
			}
			_, err := ext.Execute(context.Background(), args)
			if (mode == "ack" || mode == "audit" || mode == "deny" || mode == "approval") && err == nil {
				t.Fatal("gate failed to refuse")
			}
			if (mode == "dry_run" || mode == "plan") && err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("refusal/preview created issuance state")
			}
			if len(l.processes) != 1 {
				t.Fatal("refusal/preview spawned an issuance instance")
			}
		})
	}
}
func TestFailedIssuedInstanceIsUncertainAndNeverRestarts(t *testing.T) {
	for _, phase := range []string{"init", "call"} {
		t.Run(phase, func(t *testing.T) {
			sink := audit.NewMemory()
			s, l, root := issuanceService(t, sink)
			l.initError = phase == "init"
			l.callError = phase == "call"
			ext := NewExternalConnectorService(sink, connector.NewRegistry(), s)
			ctx, _ := redact.EnsureScope(context.Background())
			_, err := ext.Execute(ctx, issueArgs())
			if err == nil {
				t.Fatal("failure succeeded")
			}
			secret := l.processes[1].values[pluginsdk.IssuedValuePrefix+"runtime_password"]
			if secret == "" || strings.Contains(err.Error(), secret) {
				t.Fatal("issued secret escaped error")
			}
			if !l.processes[1].closed || l.processes[0].closed || !s.manager.Loaded("issuer") {
				t.Fatal("one-shot failure affected primary process")
			}
			if _, err = os.Stat(filepath.Join(root, strings.Repeat("a", 32), "uncertain.json")); err != nil {
				t.Fatal("uncertain outcome not durable")
			}
			if _, err = ext.Execute(ctx, issueArgs()); err == nil {
				t.Fatal("uncertain issuance replayed")
			}
		})
	}
}

func TestDirectPluginIssuanceUsesSameBrokerAndCannotBypassAck(t *testing.T) {
	s, l, root := issuanceService(t, audit.NewMemory())
	a := issueArgs()
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	args := PluginConnectorExecArgs{Operation: a.Operation, Config: a.Config}
	if _, err := s.Execute(ctx, a.Connector, args); err == nil {
		t.Fatal("direct route bypassed ack")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("direct refusal created credentials")
	}
	args.Acknowledged = true
	out, err := s.Execute(ctx, a.Connector, args)
	if err != nil || out.CredentialIssuance == nil || len(l.processes) != 2 {
		t.Fatalf("direct issuance failed: %v", err)
	}
}

func TestCredentialPersistenceFailureNeverSpawnsIssuedPlugin(t *testing.T) {
	s, l, root := issuanceService(t, audit.NewMemory())
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secrets"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	ext := NewExternalConnectorService(audit.NewMemory(), connector.NewRegistry(), s)
	if _, err := ext.Execute(context.Background(), issueArgs()); err == nil {
		t.Fatal("store failure ignored")
	}
	if len(l.processes) != 1 {
		t.Fatal("plugin launched without persisted credentials")
	}
}

func TestIssuingOperationRefusesAnUnbrokeredDispatch(t *testing.T) {
	s, l, root := issuanceService(t, audit.NewMemory())
	a := issueArgs()
	_, err := s.manager.ExecuteOperation(context.Background(), pluginhost.OperationArgs{Connector: a.Connector, Operation: a.Operation, Config: a.Config, Acknowledged: true})
	if err == nil || len(l.processes) != 1 {
		t.Fatal("unbrokered issuing write was dispatched")
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("raw plugin host created issuance state")
	}
}

func TestIssuedSlotNamesSurviveDiscoveryWireRendering(t *testing.T) {
	s, _, _ := issuanceService(t, audit.NewMemory())
	rec := httptest.NewRecorder()
	w, _ := BeginHTTPRequest(rec, httptest.NewRequest(http.MethodGet, "/connectors", nil), SurfaceSocket)
	writeJSON(w, http.StatusOK, redact.DeclaredSchema{Value: s.Definitions()})
	var defs []contract.Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &defs); err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || len(defs[0].Operations) != 1 || len(defs[0].Operations[0].IssuedSecrets) != 1 || defs[0].Operations[0].IssuedSecrets[0] != "runtime_password" {
		t.Fatal("issued slot declaration lost in discovery response")
	}
}

func TestUnloadingPluginCancelsIssuedOperation(t *testing.T) {
	s, l, root := issuanceService(t, audit.NewMemory())
	l.callStarted = make(chan struct{})
	ext := NewExternalConnectorService(audit.NewMemory(), connector.NewRegistry(), s)
	done := make(chan error, 1)
	go func() { _, err := ext.Execute(context.Background(), issueArgs()); done <- err }()
	select {
	case <-l.callStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("issued operation did not start")
	}
	if err := s.manager.Unload(context.Background(), "issuer"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unload did not cancel issuance")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("issued operation outlived plugin unload")
	}
	if !l.processes[1].closed {
		t.Fatal("issued instance was left running")
	}
	if _, err := os.Stat(filepath.Join(root, strings.Repeat("a", 32), "uncertain.json")); err != nil {
		t.Fatal("canceled issuance lost uncertainty")
	}
}
