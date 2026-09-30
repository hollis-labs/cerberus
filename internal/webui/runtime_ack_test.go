package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/infra"
)

// The console sends acknowledged only from its confirm step. A resource
// action or pipeline run without it is refused with 409 and the refusal's
// message; with it, the call reaches the runtime.
func TestWebRuntimeActionsNeedTheConfirmStep(t *testing.T) {
	runtime := cerbapi.NewResourceRuntimeService(audit.NewMemory(), cerbapi.WithResourceRuntimeConfigV2(&config.ConfigV2{}))
	handler := signedIn(t, mustNew(t, cerbapi.NewInProcessClient(cerbapi.WithResourceRuntimeService(runtime))), testGuard())
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
	// The run goes to the daemon (CERB-GAP-886); an in-process client
	// stands in for it, with the same gate.
	sink := audit.NewMemory()
	client := cerbapi.NewInProcessClient(cerbapi.WithConfigPath(cfgPath), cerbapi.WithInProcessAudit(sink),
		cerbapi.WithResourceRuntimeService(cerbapi.NewResourceRuntimeService(sink, cerbapi.WithResourceRuntimeConfigV2(&config.ConfigV2{}))))
	srv, err := New(client, sink, cfgPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := signedIn(t, srv, testGuard())
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

// A profile's labels are saved and read back, and a misspelled one is
// refused rather than quietly reading as unknown.
func TestDeploymentProfileLabels(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	sink := audit.NewMemory()
	srv, err := New(&fakeClient{consoleWrites: consoleDaemon(cfgPath, sink, nil)}, sink, cfgPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := signedIn(t, srv, testGuard())
	token := sessionToken(t, handler)
	save := func(body string) *httptest.ResponseRecorder {
		req := newTestRequest(http.MethodPost, "/api/deployments", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cerberus-Web-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	base := `"id":"site","name":"Site","provider":"vercel","repo_path":"/tmp/site"`
	if rec := save(`{` + base + `,"env":"devv"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "labels") {
		t.Fatalf("a misspelled env: %d %s", rec.Code, rec.Body.String())
	}
	if rec := save(`{` + base + `,"env":"dev","owner":"self","admin":"self","tags":["web"]}`); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	state, err := infra.LoadState(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := state.Profile("site")
	if !ok || p.Env != "dev" || p.Owner != "self" || p.Admin.Default != "self" || len(p.Tags) != 1 {
		t.Fatalf("saved %+v", p)
	}
}

// The credential editor lists every connector that declares a secret, with
// each secret's name, kind and whether a value is stored, never the value;
// its lists are never null, and a connector declaring nothing is left out.
func TestCredentialEditorListsDeclaredSecrets(t *testing.T) {
	const stored = "cf-token-sentinel-0123456789"
	secrets := &memorySecrets{values: map[string]string{"cloudflare/api_token": stored}}
	srv, err := New(&fakeClient{connectors: credentialFixtures()}, audit.NewMemory(), filepath.Join(t.TempDir(), "config.yaml"), secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := signedIn(t, srv, testGuard())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newTestRequest(http.MethodGet, "/api/credentials", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "null") || strings.Contains(body, stored) || strings.Contains(body, "[REDACTED]") {
		t.Fatalf("credentials: %d %s", rec.Code, body)
	}
	var resp credentialsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := credentialsResponse{Providers: []credentialProviderDTO{
		{ID: "cloudflare", Version: "0.2.0", Secrets: []credentialSecretDTO{
			{Name: "api_token", Description: "Cloudflare API token.", Env: "CERBERUS_CLOUDFLARE_API_TOKEN", Required: true, Kind: "credential", Present: true},
		}},
		{ID: "namecheap", Version: "0.2.1", Secrets: []credentialSecretDTO{
			{Name: "api_user", Description: "Namecheap API user.", Kind: "name"},
			{Name: "api_key", Description: "Namecheap API key.", Kind: "credential"},
			{Name: "username", Description: "Namecheap username.", Kind: "name"},
			{Name: "client_ip", Description: "The address on the account's API allow-list.", Kind: "name"},
		}},
	}}
	if !reflect.DeepEqual(resp, want) {
		t.Fatalf("credentials = %+v\nwant %+v", resp, want)
	}
}

// A resource action, pipeline run or deploy-profile run retried after its
// out-of-band approval names that approval, and the daemon is sent it;
// without one, none.
func TestAMutationRetryNamesItsApproval(t *testing.T) {
	for body, want := range map[string]string{
		`{"acknowledged":true,"approval_id":"apr_1"}`: "apr_1",
		`{"acknowledged":true}`:                       "",
	} {
		req := newTestRequest(http.MethodPost, "/api/resources/web/stop", strings.NewReader(body))
		opts, err := decodeMutationBody(req)
		if err != nil {
			t.Fatal(err)
		}
		if got := cerbapi.ApplyMutationOptions(opts); got.ApprovalID != want || !got.Acknowledged {
			t.Fatalf("%s: %+v", body, got)
		}
	}
}
