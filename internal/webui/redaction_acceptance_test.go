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

// WP-S2 acceptance, console half: the console runs a connector operation
// in-process, the GitHub connector resolves sentinel.Value, go-github
// composes a 401 around it, and the console's error body does not carry it.
// The other surfaces are checked in cmd/cerberus.
func TestConsoleNeverShowsAResolvedCredential(t *testing.T) {
	api := sentinel.GitHubAPI(t)
	sink := audit.NewMemory()
	svc := cerbapi.NewExternalConnectorService(sink, sentinel.Registry(t, sentinel.Provider(), api.URL))
	handler := signedIn(t, mustNew(t, cerbapi.NewInProcessClient(cerbapi.WithExternalConnectorService(svc), cerbapi.WithInProcessAudit(sink))), testGuard())

	req := newTestRequest(http.MethodPost, "/api/connectors/github/operations/status",
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
