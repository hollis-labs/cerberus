package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func mustNew(t *testing.T, client cerbapi.Client) *Server {
	t.Helper()
	srv, err := New(client, "", nil, nil)
	if err != nil {
		t.Fatalf("webui.New: %v", err)
	}
	return srv
}

func TestStateChangingResourceActionsRequireSessionToken(t *testing.T) {
	client := &fakeClient{}
	handler := mustNew(t, client).Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/resources/app/apply", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without token status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if client.applyCalls != 0 {
		t.Fatalf("apply should not be called without token")
	}

	sessionReq := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	sessionRec := httptest.NewRecorder()
	handler.ServeHTTP(sessionRec, sessionReq)
	if sessionRec.Code != http.StatusOK {
		t.Fatalf("session status = %d, want %d", sessionRec.Code, http.StatusOK)
	}
	var session struct {
		ActionToken string `json:"action_token"`
	}
	if err := json.NewDecoder(sessionRec.Body).Decode(&session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if session.ActionToken == "" {
		t.Fatal("expected non-empty action token")
	}

	req = httptest.NewRequest(http.MethodPost, "/api/resources/app/apply", strings.NewReader("{}"))
	req.Host = "127.0.0.1:9090"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://127.0.0.1:9090")
	req.Header.Set("X-Cerberus-Web-Token", session.ActionToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST with token status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if client.applyCalls != 1 {
		t.Fatalf("apply calls = %d, want 1", client.applyCalls)
	}
}

func TestStateChangingResourceStopRequiresSessionToken(t *testing.T) {
	client := &fakeClient{}
	handler := mustNew(t, client).Handler()

	sessionReq := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	sessionRec := httptest.NewRecorder()
	handler.ServeHTTP(sessionRec, sessionReq)
	if sessionRec.Code != http.StatusOK {
		t.Fatalf("session status = %d, want %d", sessionRec.Code, http.StatusOK)
	}
	var session struct {
		ActionToken string `json:"action_token"`
	}
	if err := json.NewDecoder(sessionRec.Body).Decode(&session); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/resources/app/stop", strings.NewReader("{}"))
	req.Host = "127.0.0.1:9090"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://127.0.0.1:9090")
	req.Header.Set("X-Cerberus-Web-Token", session.ActionToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST stop with token status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if client.stopCalls != 1 {
		t.Fatalf("stop calls = %d, want 1", client.stopCalls)
	}
}

func TestHandleResourcesReturnsServiceUnavailableForDaemonDialFailure(t *testing.T) {
	client := &fakeClient{
		listResourcesErr: &cerbapi.DaemonUnreachableError{
			Path: "/tmp/cerberus.sock",
			Err:  errors.New("dial unix /tmp/cerberus.sock: connect: no such file or directory"),
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/resources", nil)
	rec := httptest.NewRecorder()

	mustNew(t, client).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cerberus daemon unavailable or not responding") {
		t.Fatalf("body = %q, want daemon unavailable message", rec.Body.String())
	}
}

func TestHandleResourcesReturnsServiceUnavailableForTimeout(t *testing.T) {
	client := &fakeClient{listResourcesErr: context.DeadlineExceeded}
	req := httptest.NewRequest(http.MethodGet, "/api/resources", nil)
	rec := httptest.NewRecorder()

	mustNew(t, client).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "timed out while gathering resource state") {
		t.Fatalf("body = %q, want timeout message", rec.Body.String())
	}
}

func sessionToken(t *testing.T, handler http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/session", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("session status = %d, want %d", rec.Code, http.StatusOK)
	}
	var session struct {
		ActionToken string `json:"action_token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return session.ActionToken
}

func TestDomainReadEndpointsReachable(t *testing.T) {
	handler := mustNew(t, &fakeClient{}).Handler()
	paths := []string{
		"/api/settings",
		"/api/health",
		"/api/health?resource=app",
		"/api/projects",
		"/api/pipelines",
		"/api/config/validate",
		"/api/config/resolve",
		"/api/config/backups",
		"/api/infra",
		"/api/deployments",
		"/api/connectors",
		"/api/plugins/connectors",
		"/api/resources/app/inspect",
		"/api/resources/app/doctor",
		"/api/plugins/connectors/pl/health",
	}
	for _, p := range paths {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want %d; body=%s", p, rec.Code, http.StatusOK, rec.Body.String())
		}
	}
}

func TestDomainMutatingEndpointsRequireToken(t *testing.T) {
	handler := mustNew(t, &fakeClient{}).Handler()
	token := sessionToken(t, handler)
	paths := []string{
		"/api/resources/app/sync",
		"/api/resources/app/remove",
		"/api/pipelines/p1/run",
		"/api/config/migrate",
		"/api/config/backups/restore",
		"/api/infra/providers/vercel",
		"/api/deployments",
		"/api/deployments/chrispian-dev/delete",
		"/api/deployments/chrispian-dev/run",
		"/api/connectors/c1/operations/list",
		"/api/plugins/connectors/health",
		"/api/plugins/connectors/operations/list",
		"/api/plugins/connectors/install",
		"/api/plugins/connectors/pl/load",
		"/api/plugins/connectors/pl/unload",
		"/api/plugins/connectors/pl/operations/list",
	}
	for _, p := range paths {
		// Without token: rejected.
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s without token status = %d, want %d", p, rec.Code, http.StatusForbidden)
		}

		// With token: allowed through to the client.
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		req.Host = "127.0.0.1:9090"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://127.0.0.1:9090")
		req.Header.Set("X-Cerberus-Web-Token", token)
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Errorf("POST %s with token status = %d, want non-%d; body=%s", p, rec.Code, http.StatusForbidden, rec.Body.String())
		}
	}
}

type fakeClient struct {
	applyCalls       int
	stopCalls        int
	listResourcesErr error
}

func (f *fakeClient) ResourceLogs(context.Context, string, int, string) (*cerbapi.LogLines, error) {
	return &cerbapi.LogLines{}, nil
}

func (f *fakeClient) Health(context.Context, string) (*cerbapi.DaemonHealth, error) {
	return &cerbapi.DaemonHealth{}, nil
}

func (f *fakeClient) ListProjects(context.Context) ([]cerbapi.ProjectInfo, error) {
	return nil, nil
}

func (f *fakeClient) ResolveDiagnostics(context.Context) (*cerbapi.ResolveDiagnostics, error) {
	return &cerbapi.ResolveDiagnostics{}, nil
}

func (f *fakeClient) ListResources(context.Context, cerbapi.ResourceListArgs) ([]cerbapi.ResourceInfo, error) {
	if f.listResourcesErr != nil {
		return nil, f.listResourcesErr
	}
	return nil, nil
}

func (f *fakeClient) GetResourceRuntime(context.Context, string) (*cerbapi.ResourceRuntimeStatus, error) {
	return &cerbapi.ResourceRuntimeStatus{}, nil
}

func (f *fakeClient) GetResourceInspect(context.Context, string) (*cerbapi.ResourceInspect, error) {
	return &cerbapi.ResourceInspect{}, nil
}

func (f *fakeClient) GetResourceDoctor(context.Context, string) (*cerbapi.ResourceDoctor, error) {
	return &cerbapi.ResourceDoctor{}, nil
}

func (f *fakeClient) DeployResource(context.Context, string, ...cerbapi.DeployResourceOption) (*cerbapi.OpResult, error) {
	return &cerbapi.OpResult{Success: true}, nil
}

func (f *fakeClient) ApplyResource(context.Context, string) (*cerbapi.OpResult, error) {
	f.applyCalls++
	return &cerbapi.OpResult{Success: true}, nil
}

func (f *fakeClient) ReloadResource(context.Context, string) (*cerbapi.OpResult, error) {
	return &cerbapi.OpResult{Success: true}, nil
}

func (f *fakeClient) StopResource(context.Context, string) (*cerbapi.OpResult, error) {
	f.stopCalls++
	return &cerbapi.OpResult{Success: true}, nil
}

func (f *fakeClient) SyncResource(context.Context, string) (*cerbapi.OpResult, error) {
	return &cerbapi.OpResult{Success: true}, nil
}

func (f *fakeClient) RemoveResource(context.Context, string) (*cerbapi.OpResult, error) {
	return &cerbapi.OpResult{Success: true}, nil
}

func (f *fakeClient) ListPipelines(context.Context) ([]cerbapi.PipelineInfo, error) {
	return nil, nil
}

func (f *fakeClient) RunPipeline(context.Context, string) (*cerbapi.PipelineRunResult, error) {
	return &cerbapi.PipelineRunResult{}, nil
}

func (f *fakeClient) ListConnectors(context.Context) ([]contract.Definition, error) {
	return nil, nil
}

func (f *fakeClient) ListLiveConnectors(context.Context) ([]string, error) {
	return nil, nil
}

func (f *fakeClient) ExecuteConnectorOperation(context.Context, cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	return cerbapi.ExternalConnectorOperationResult{}, nil
}

func (f *fakeClient) PluginHealth(context.Context, cerbapi.PluginConnectorHealthArgs) (cerbapi.PluginConnectorHealth, error) {
	return cerbapi.PluginConnectorHealth{}, nil
}

func (f *fakeClient) ExecutePluginConnector(context.Context, cerbapi.PluginConnectorExecArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	return cerbapi.ExternalConnectorOperationResult{}, nil
}

func (f *fakeClient) InstallManagedPlugin(context.Context, cerbapi.PluginConnectorHealthArgs) (cerbapi.ManagedPluginConnectorState, error) {
	return cerbapi.ManagedPluginConnectorState{}, nil
}

func (f *fakeClient) LoadManagedPlugin(context.Context, string) (cerbapi.ManagedPluginConnectorState, error) {
	return cerbapi.ManagedPluginConnectorState{}, nil
}

func (f *fakeClient) UnloadManagedPlugin(context.Context, string) (cerbapi.ManagedPluginConnectorState, error) {
	return cerbapi.ManagedPluginConnectorState{}, nil
}

func (f *fakeClient) UninstallManagedPlugin(context.Context, string) (cerbapi.ManagedPluginConnectorState, error) {
	return cerbapi.ManagedPluginConnectorState{}, nil
}

func (f *fakeClient) ListManagedPlugins(context.Context) ([]cerbapi.ManagedPluginConnectorState, error) {
	return nil, nil
}

func (f *fakeClient) ManagedPluginHealth(context.Context, string) (cerbapi.PluginConnectorHealth, error) {
	return cerbapi.PluginConnectorHealth{}, nil
}

func (f *fakeClient) ExecuteManagedPlugin(context.Context, string, cerbapi.PluginConnectorExecArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	return cerbapi.ExternalConnectorOperationResult{}, nil
}

func (*fakeClient) GetPipeline(context.Context, string) (*cerbapi.PipelineDetail, error) {
	return nil, nil
}
