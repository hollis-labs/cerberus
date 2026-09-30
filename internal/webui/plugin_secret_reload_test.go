package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

type memorySecrets struct{ values map[string]string }

func (m *memorySecrets) Get(_ context.Context, service, key string) (string, error) {
	return m.values[service+"/"+key], nil
}
func (m *memorySecrets) Set(_ context.Context, service, key, value string) error {
	m.values[service+"/"+key] = value
	return nil
}
func (m *memorySecrets) Delete(_ context.Context, service, key string) error {
	delete(m.values, service+"/"+key)
	return nil
}

// reloadingClient is a daemon with one loaded managed plugin, recording the
// lifecycle calls a console secret save makes.
type reloadingClient struct {
	*fakeClient
	plugins  []cerbapi.ManagedPluginConnectorState
	calls    []string
	surfaces []cerbapi.CallerSurface
	loadErr  error
}

func (c *reloadingClient) ListManagedPlugins(context.Context) ([]cerbapi.ManagedPluginConnectorState, error) {
	return c.plugins, nil
}
func (c *reloadingClient) UnloadManagedPlugin(ctx context.Context, id string) (cerbapi.ManagedPluginConnectorState, error) {
	c.calls = append(c.calls, "unload "+id)
	c.surfaces = append(c.surfaces, cerbapi.CallerSurfaceFrom(ctx))
	return cerbapi.ManagedPluginConnectorState{ID: id}, nil
}
func (c *reloadingClient) LoadManagedPlugin(ctx context.Context, id string) (cerbapi.ManagedPluginConnectorState, error) {
	c.calls = append(c.calls, "load "+id)
	c.surfaces = append(c.surfaces, cerbapi.CallerSurfaceFrom(ctx))
	return cerbapi.ManagedPluginConnectorState{ID: id, Loaded: true}, c.loadErr
}

func saveProviderSecret(t *testing.T, client cerbapi.Client, provider, body string) map[string]any {
	t.Helper()
	secrets := &memorySecrets{values: map[string]string{}}
	cfgPath, sink := filepath.Join(t.TempDir(), "config.yaml"), audit.NewMemory()
	if r, ok := client.(*reloadingClient); ok {
		r.consoleWrites = consoleDaemon(cfgPath, sink, secrets)
	}
	srv, err := New(client, sink, cfgPath, secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetSecretStore(secrets)
	handler := signedIn(t, srv, testGuard())
	req := newTestRequest(http.MethodPost, "/api/credentials/"+provider, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cerberus-Web-Token", sessionToken(t, handler))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// A plugin receives its credentials at load, so a console save reloads the
// loaded plugin with that id. Otherwise the new token would sit unused until
// someone ran `managed load`, where the built-in picked it up on the next call.
func TestConsoleSecretSaveReloadsTheLoadedPlugin(t *testing.T) {
	client := &reloadingClient{fakeClient: &fakeClient{}, plugins: []cerbapi.ManagedPluginConnectorState{{ID: "cloudflare", Loaded: true}}}
	resp := saveProviderSecret(t, client, "cloudflare", `{"secrets":{"api_token":"new-token-value"}}`)
	if resp["plugin_reloaded"] != true {
		t.Fatalf("response = %v, want plugin_reloaded true", resp)
	}
	if strings.Join(client.calls, ",") != "unload cloudflare,load cloudflare" {
		t.Fatalf("calls = %v", client.calls)
	}
	// The reload travels on the request's context, so the daemon records it
	// as the web console's (TestConsoleReloadIsAuditedAsWeb in cerbapi).
	for _, surface := range client.surfaces {
		if surface != cerbapi.SurfaceWeb {
			t.Fatalf("surfaces = %v, want the reload attributed to the web console", client.surfaces)
		}
	}
}

func TestConsoleSecretSaveLeavesOtherPluginsAlone(t *testing.T) {
	for name, plugins := range map[string][]cerbapi.ManagedPluginConnectorState{
		"no plugin with the id": {{ID: "contextforge", Loaded: true}},
		"installed, not loaded": {{ID: "cloudflare", Loaded: false}},
	} {
		t.Run(name, func(t *testing.T) {
			client := &reloadingClient{fakeClient: &fakeClient{}, plugins: plugins}
			resp := saveProviderSecret(t, client, "cloudflare", `{"secrets":{"api_token":"v"}}`)
			if resp["plugin_reloaded"] != false || len(client.calls) != 0 {
				t.Fatalf("response = %v, calls = %v; want no reload", resp, client.calls)
			}
		})
	}
}

// A save with no secret change touches no plugin: a blank value keeps the
// stored one.
func TestConsoleUnchangedSaveDoesNotReload(t *testing.T) {
	client := &reloadingClient{fakeClient: &fakeClient{}, plugins: []cerbapi.ManagedPluginConnectorState{{ID: "cloudflare", Loaded: true}}}
	resp := saveProviderSecret(t, client, "cloudflare", `{"secrets":{"api_token":""}}`)
	if _, reported := resp["plugin_reloaded"]; reported || len(client.calls) != 0 {
		t.Fatalf("response = %v, calls = %v", resp, client.calls)
	}
}

// A failed reload does not fail the save, and says how to recover.
func TestConsoleSecretSaveReportsAFailedReload(t *testing.T) {
	client := &reloadingClient{fakeClient: &fakeClient{}, plugins: []cerbapi.ManagedPluginConnectorState{{ID: "cloudflare", Loaded: true}}, loadErr: errors.New("subprocess exited")}
	resp := saveProviderSecret(t, client, "cloudflare", `{"secrets":{"api_token":"v"}}`)
	msg, _ := resp["plugin_reload_error"].(string)
	if resp["success"] != true || !strings.Contains(msg, "cerberus connectors plugin managed load cloudflare") {
		t.Fatalf("response = %v", resp)
	}
}

// The console's Namecheap client IP goes where the namecheap plugin reads it:
// the secret chain as namecheap/client_ip, because the plugin declares it as
// a secret. It used to be saved as a field in infra.yaml, which nothing read,
// so the plugin fell back to 127.0.0.1; now the editor offers exactly what
// the plugin declares.
func TestConsoleSavesNamecheapClientIPWhereThePluginReadsIt(t *testing.T) {
	secrets := &memorySecrets{values: map[string]string{}}
	cfgPath, sink := filepath.Join(t.TempDir(), "config.yaml"), audit.NewMemory()
	srv, err := New(&reloadingClient{fakeClient: &fakeClient{connectors: credentialFixtures(), consoleWrites: consoleDaemon(cfgPath, sink, secrets)}}, sink, cfgPath, secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetSecretStore(secrets)
	handler := signedIn(t, srv, testGuard())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newTestRequest(http.MethodGet, "/api/credentials", nil))
	var listed credentialsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, p := range listed.Providers {
		for _, secret := range p.Secrets {
			found = found || (p.ID == "namecheap" && secret.Name == "client_ip" && secret.Kind == "name")
		}
	}
	if !found {
		t.Fatalf("namecheap's declared client_ip is not in the editor: %s", rec.Body.String())
	}

	req := newTestRequest(http.MethodPost, "/api/credentials/namecheap", strings.NewReader(`{"secrets":{"client_ip":"203.0.113.7"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cerberus-Web-Token", sessionToken(t, handler))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || secrets.values["namecheap/client_ip"] != "203.0.113.7" {
		t.Fatalf("status %d, stored %v; want namecheap/client_ip in the secret store", rec.Code, secrets.values)
	}
}
