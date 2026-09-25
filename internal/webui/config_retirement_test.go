package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCentralizedMigrationEndpointsAreGone(t *testing.T) {
	handler := mustNew(t, &fakeClient{}).Handler(testGuard())
	token := sessionToken(t, handler)
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodGet, "/api/config/migrate/preview"},
		{http.MethodPost, "/api/config/migrate"},
	} {
		request := newTestRequest(endpoint.method, endpoint.path, strings.NewReader("{}"))
		request.Host = "127.0.0.1:9090"
		request.Header.Set("X-Cerberus-Web-Token", token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), "centralized config migration is retired") {
			t.Fatalf("%s: %d %s", endpoint.path, response.Code, response.Body.String())
		}
	}
}
