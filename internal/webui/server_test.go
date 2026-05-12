package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

func TestStateChangingResourceActionsRequireSessionToken(t *testing.T) {
	client := &fakeClient{}
	handler := New(client, nil).Handler()

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

type fakeClient struct {
	applyCalls int
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

func (f *fakeClient) ListResources(context.Context, cerbapi.ResourceListArgs) ([]cerbapi.ResourceInfo, error) {
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

func (f *fakeClient) DeployResource(context.Context, string) (*cerbapi.OpResult, error) {
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
