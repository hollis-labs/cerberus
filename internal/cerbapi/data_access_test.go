package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/connector"
	"github.com/hollis-labs/cerberus/internal/dataaccess"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	sdk "github.com/hollis-labs/cerberus/pkg/plugin"
)

type dataProcess struct {
	config         map[string]string
	called, closed bool
}

func (p *dataProcess) Init(_ context.Context, v pluginhost.SDKInitParams) (pluginhost.SDKInitResult, error) {
	p.config = v.Config
	return pluginhost.SDKInitResult{ID: "data", Version: "dev", Protocol: pluginhost.SDKProtocolVersion, CapabilityContract: 1}, nil
}
func (p *dataProcess) Load(context.Context) (pluginhost.SDKLoadResult, error) {
	return pluginhost.SDKLoadResult{}, nil
}
func (p *dataProcess) Unload(context.Context) error { return nil }
func (p *dataProcess) Health(context.Context) (pluginhost.SDKHealthResult, error) {
	return pluginhost.SDKHealthResult{OK: true}, nil
}
func (p *dataProcess) Close() error { p.closed = true; return nil }
func (p *dataProcess) CallTool(_ context.Context, r pluginhost.SDKMCPCallRequest) (pluginhost.SDKMCPCallResult, error) {
	p.called = true
	var permit sdk.DataPermit
	var q sdk.DataQuery
	b, _ := json.Marshal(r.Arguments)
	_ = json.Unmarshal(b, &q)
	if json.Unmarshal([]byte(p.config[sdk.DataPermitConfigKey]), &permit) != nil || permit.Validate(q, time.Now()) != nil {
		return pluginhost.SDKMCPCallResult{}, errors.New("no private permit")
	}
	return pluginhost.SDKMCPCallResult{Content: json.RawMessage(`{"columns":["id"],"rows":[[9223372036854775807]]}`)}, nil
}

type dataLauncher struct{ processes []*dataProcess }

func (l *dataLauncher) Launch(context.Context, pluginhost.InstalledPlugin) (pluginhost.Process, error) {
	p := &dataProcess{}
	l.processes = append(l.processes, p)
	return p, nil
}
func TestDataPluginRequiresInternalProofAndUsesOneShotInit(t *testing.T) {
	sink := audit.NewMemory()
	s, err := NewManagedPluginConnectorService(sink, "test", io.Discard, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	launcher := &dataLauncher{}
	s.manager = newTestPluginManager(t, nil, launcher, "test")
	manifest := contract.Manifest{APIVersion: contract.ManifestAPIVersion, Kind: "Connector", ID: "data", Version: "dev", ResourceTypes: []string{"server"}, Operations: []contract.ManifestOperation{{Name: "query", Effect: contract.EffectReadSensitive, Preview: contract.PreviewNone, LocalFS: contract.LocalFSNone, Output: contract.OutputStructured, Target: contract.TargetDescriptor{Kind: "postgres.data", From: []string{"target"}}, InputSchema: contract.ObjectSchema(map[string]any{"target": contract.StringSchema("target"), "projection": contract.StringSchema("projection"), "version": contract.StringSchema("version"), "parameters": map[string]any{"type": "object"}}, "target", "projection", "version", "parameters")}}}
	s.manager.RegisterInstalled(pluginhost.InstalledPlugin{ID: "data", Version: "dev", Origin: pluginhost.OriginInstalled, Path: t.TempDir(), Manifest: manifest, Spec: pluginhost.PluginYAML{Capabilities: []pluginhost.CapabilityRequest{{Name: sdk.DataQueryCapability}}}})
	if err = s.manager.Load(context.Background(), "data"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.manager.Unload(context.Background(), "data") })
	ext := NewExternalConnectorService(sink, connector.NewRegistry(), s)
	q := sdk.DataQuery{Target: "fixture", Projection: "documents", Version: "v1", Parameters: json.RawMessage(`{}`)}
	args := dataArgs("data", q)
	if _, err = ext.Execute(context.Background(), args); err == nil {
		t.Fatal("generic connector bypassed data grant")
	}
	if _, err = s.Execute(context.Background(), "data", PluginConnectorExecArgs{Operation: "query", Config: args.Config}); err == nil {
		t.Fatal("direct managed route bypassed data grant")
	}
	if len(launcher.processes) != 1 || launcher.processes[0].called {
		t.Fatal("unverified request reached plugin")
	}
	proof := dataaccess.Proof{Connector: "data", Query: q, Permit: sdk.DataPermit{Digest: sdk.DataQueryDigest(q), ExpiresAt: time.Now().Add(2 * time.Second), MaxRows: 100, MaxBytes: 1 << 20}}
	ctx := dataaccess.WithProof(context.Background(), proof)
	out, err := ext.Execute(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if out.Data == nil || len(launcher.processes) != 2 || !launcher.processes[1].called || !launcher.processes[1].closed || launcher.processes[0].config[sdk.DataPermitConfigKey] != "" {
		t.Fatal("private one-shot lifecycle failed")
	}
	encoded, _ := json.Marshal(out.Data)
	if !strings.Contains(string(encoded), "9223372036854775807") {
		t.Fatal("data integer precision lost")
	}
	args.Config["projection"] = "different"
	if _, err = ext.Execute(ctx, args); err == nil {
		t.Fatal("proof reused for a different projection")
	}
}
func TestDataClientNeverFallsBackToOlderDaemon(t *testing.T) {
	var generic bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/data/v1/query" {
			http.NotFound(w, r)
			return
		}
		generic = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := &SocketClient{http: srv.Client(), baseURL: srv.URL}
	if _, err := c.QueryData(context.Background(), "opaque", dataaccess.Request{}); err == nil || generic {
		t.Fatal("older daemon did not fail closed")
	}
}
func TestDataHTTPRequiresTypedBoundedRequests(t *testing.T) {
	for _, body := range []string{`{"grant_id":"g","sql":"select 1"}`, `{} {}`, `{"parameters":`} {
		r := httptest.NewRequest("POST", "/data/v1/query", nil)
		r.Body = io.NopCloser(strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer opaque")
		w := httptest.NewRecorder()
		DataHTTP(&dataaccess.Service{}).ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("malformed request: %d", w.Code)
		}
	}
}
