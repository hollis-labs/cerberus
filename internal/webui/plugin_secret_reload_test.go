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
	srv, err := New(client, filepath.Join(t.TempDir(), "config.yaml"), &memorySecrets{values: map[string]string{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler(testGuard())
	req := newTestRequest(http.MethodPost, "/api/infra/providers/"+provider, strings.NewReader(body))
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

// A save with no secret change touches no plugin.
func TestConsoleFieldOnlySaveDoesNotReload(t *testing.T) {
	client := &reloadingClient{fakeClient: &fakeClient{}, plugins: []cerbapi.ManagedPluginConnectorState{{ID: "cloudflare", Loaded: true}}}
	resp := saveProviderSecret(t, client, "cloudflare", `{"values":{"account_id":"acct"}}`)
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
