package cerbapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/connector"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// succeedingEchoProcess is a plugin whose operation succeeds and returns the
// credential it was handed — the case the plugin redactor never sees,
// because it only runs over failures.
type succeedingEchoProcess struct{ echoingPluginProcess }

func (p *succeedingEchoProcess) CallTool(context.Context, pluginhost.SDKMCPCallRequest) (pluginhost.SDKMCPCallResult, error) {
	content, _ := json.Marshal(map[string]string{"echo": "authorized as " + p.token})
	return pluginhost.SDKMCPCallResult{Content: content}, nil
}

// WP-S2 acceptance for plugins: a plugin's credential, resolved at load, is
// merged into the scope of each operation that uses it, so a plugin that
// echoes it in a successful result does not put it on the socket.
func TestPluginSuccessResultNeverCarriesItsCredential(t *testing.T) {
	svc, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", io.Discard, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc.manager = pluginhost.NewManager(nil, echoingLauncher{process: &succeedingEchoProcess{}}, "test",
		pluginhost.WithSecretResolver(sentinelResolver{}))
	svc.manager.RegisterInstalled(pluginhost.InstalledPlugin{
		ID: "leaky", Origin: pluginhost.OriginInstalled,
		Manifest: contract.Manifest{
			APIVersion: contract.ManifestAPIVersion, Kind: "Connector", ID: "leaky", Version: "dev",
			Config:     contract.ConfigSchema{Secrets: []contract.SecretRequirement{{Name: "token", Env: "CERBERUS_LEAKY_TOKEN"}}},
			Operations: []contract.ManifestOperation{{Name: "list_things", Effect: contract.EffectRead, InputSchema: contract.ObjectSchema(map[string]any{})}},
		},
	})
	if err = svc.manager.Load(context.Background(), "leaky"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	ext := NewExternalConnectorService(audit.NewMemory(), connector.NewRegistry(), svc)

	// The socket's connector route renders the result with writeJSON on the
	// request it began.
	rec := httptest.NewRecorder()
	w, r := BeginHTTPRequest(rec, httptest.NewRequest(http.MethodPost, "/connectors/leaky/operations/list_things", nil), SurfaceSocket)
	result, err := ext.Execute(r.Context(), ExternalConnectorOperationArgs{Connector: "leaky", Operation: "list_things"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if data, _ := json.Marshal(result.Data); !strings.Contains(string(data), resolvedSentinel) {
		t.Fatalf("precondition: the plugin should have echoed its credential: %s", data)
	}
	writeJSON(w, http.StatusOK, result)
	body := rec.Body.String()
	assertNoPluginSentinel(t, "socket result", body)
	if !strings.Contains(body, redact.Marker) {
		t.Fatalf("no marker, so nothing was proven: %s", body)
	}
}
