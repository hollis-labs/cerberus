package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/testfixture/sentinel"
)

// WP-S2 acceptance, console half: the console runs a plugin operation
// in-process, the host hands the plugin sentinel.Value, the plugin composes a
// 401 around it, and the console's error body does not carry it. The other
// surfaces are checked in cmd/cerberus.
func TestConsoleNeverShowsAResolvedCredential(t *testing.T) {
	api := sentinel.UpstreamAPI(t)
	sink := audit.NewMemory()
	svc, managed := sentinel.Service(t, sink, sentinel.Provider(), api.URL, "TestSentinelPluginHelperProcess")
	handler := signedIn(t, mustNew(t, cerbapi.NewInProcessClient(cerbapi.WithExternalConnectorService(svc),
		cerbapi.WithManagedPluginConnectorService(managed), cerbapi.WithInProcessAudit(sink))), testGuard())

	req := newTestRequest(http.MethodPost, "/api/connectors/"+sentinel.ConnectorID+"/operations/"+sentinel.Operation,
		strings.NewReader(`{"config":{"owner":"hollis-labs","repo":"cerberus"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cerberus-Web-Token", sessionToken(t, handler))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 for the 401: %s", rec.Code, rec.Body.String())
	}
	sentinel.AssertAbsent(t, "console", rec.Body.String())
}

// TestSentinelPluginHelperProcess is the fixture plugin's entrypoint: the
// test binary re-executed by the plugin host. It does nothing in a normal run.
func TestSentinelPluginHelperProcess(*testing.T) { sentinel.ServeHelper() }
