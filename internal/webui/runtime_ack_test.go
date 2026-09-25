package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/infra"
)

// The console sends acknowledged only from its confirm step. A resource
// action or pipeline run without it is refused with 409 and the refusal's
// message; with it, the call reaches the runtime.
func TestWebRuntimeActionsNeedTheConfirmStep(t *testing.T) {
	runtime := cerbapi.NewResourceRuntimeService(cerbapi.WithResourceRuntimeConfigV2(&config.ConfigV2{}))
	handler := mustNew(t, cerbapi.NewInProcessClient(cerbapi.WithResourceRuntimeService(runtime))).Handler(testGuard())
	token := sessionToken(t, handler)
	post := func(path, body string) *httptest.ResponseRecorder {
		req := newTestRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cerberus-Web-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	paths := []string{"/api/pipelines/p/run"}
	for _, action := range []string{"apply", "deploy", "reload", "stop", "sync", "remove"} {
		paths = append(paths, "/api/resources/svc/"+action)
	}
	for _, path := range paths {
		rec := post(path, `{}`)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"acknowledgment_required"`) || !strings.Contains(rec.Body.String(), `"message":`) {
			t.Errorf("%s unconfirmed: %d %s, want 409 acknowledgment_required", path, rec.Code, rec.Body.String())
		}
		rec = post(path, `{"acknowledged":true}`)
		if rec.Code == http.StatusConflict || strings.Contains(rec.Body.String(), "acknowledgment_required") {
			t.Errorf("%s confirmed: %d %s, the acknowledgment did not reach the runtime", path, rec.Code, rec.Body.String())
		}
	}
}

// A deployment profile run is exec. The console fetches its plan — the
// commands that will run — for the confirm step, and the run itself is
// refused until it arrives acknowledged.
func TestWebDeploymentRunConfirmsAgainstThePlan(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	repo := filepath.Join(dir, "site")
	if err := os.MkdirAll(filepath.Join(repo, ".vercel"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".vercel", "project.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := infra.SaveState(cfgPath, &infra.State{Version: 1, Profiles: []infra.DeploymentProfile{
		{ID: "site", Name: "site", Provider: "vercel", RepoPath: repo, BuildCommand: "true", DeployCommand: "echo deployed"},
	}}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(&fakeClient{}, cfgPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler(testGuard())
	token := sessionToken(t, handler)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newTestRequest(http.MethodGet, "/api/deployments/site/plan", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"command":"true"`) || !strings.Contains(rec.Body.String(), `"command":"echo deployed"`) {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body.String())
	}

	run := func(body string) *httptest.ResponseRecorder {
		req := newTestRequest(http.MethodPost, "/api/deployments/site/run", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cerberus-Web-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := run(`{}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "acknowledgment_required") {
		t.Fatalf("unconfirmed run: %d %s, want 409", rec.Code, rec.Body.String())
	}
	if rec := run(`{"acknowledged":true}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("confirmed run: %d %s", rec.Code, rec.Body.String())
	}
}
