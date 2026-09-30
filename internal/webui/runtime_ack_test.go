package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/secrets"
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
			{Name: "api_token", Description: "Cloudflare API token.", Env: "CERBERUS_CLOUDFLARE_API_TOKEN", Required: true, Kind: "credential", Present: true, Stored: "stored"},
		}},
		{ID: "namecheap", Version: "0.2.1", Secrets: []credentialSecretDTO{
			{Name: "api_user", Description: "Namecheap API user.", Kind: "name", Stored: "missing"},
			{Name: "api_key", Description: "Namecheap API key.", Kind: "credential", Stored: "missing"},
			{Name: "username", Description: "Namecheap username.", Kind: "name", Stored: "missing"},
			{Name: "client_ip", Description: "The address on the account's API allow-list.", Kind: "name", Stored: "missing"},
		}},
	}}
	if !reflect.DeepEqual(resp, want) {
		t.Fatalf("credentials = %+v\nwant %+v", resp, want)
	}
}

// A resource action or pipeline run retried after its out-of-band approval
// names that approval, and the daemon is sent it;
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

// referenceOnly is a reader whose secret is a reference: presence says so,
// and resolving it fails the test.
type referenceOnly struct{ t *testing.T }

func (r referenceOnly) Get(context.Context, string, string) (string, error) {
	r.t.Error("the credential editor resolved a secret to draw the page")
	return "", nil
}
func (referenceOnly) Presence(context.Context, string, string) (secrets.Presence, error) {
	return secrets.PresenceReference, nil
}

// The editor reports a reference as one, and does not resolve it: a vault
// is not called on a page view.
func TestCredentialEditorNeverResolvesAReference(t *testing.T) {
	srv, err := New(&fakeClient{connectors: credentialFixtures()}, audit.NewMemory(), filepath.Join(t.TempDir(), "config.yaml"), referenceOnly{t}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := signedIn(t, srv, testGuard())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newTestRequest(http.MethodGet, "/api/credentials", nil))
	var resp credentialsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Providers) == 0 {
		t.Fatalf("credentials: %d %s", rec.Code, rec.Body.String())
	}
	for _, p := range resp.Providers {
		for _, s := range p.Secrets {
			if s.Stored != "reference" || !s.Present {
				t.Errorf("%s/%s = %+v, want a present reference", p.ID, s.Name, s)
			}
		}
	}
}
