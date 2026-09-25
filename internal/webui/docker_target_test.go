package webui

import (
	"context"
	"github.com/hollis-labs/cerberus/internal/audit"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/connector"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
)

type recordingDockerBackend struct {
	upFile  string
	started string
}

func (b *recordingDockerBackend) WithTarget(dockerconn.Target) dockerconn.Backend { return b }
func (b *recordingDockerBackend) ListContainers(context.Context) ([]dockerconn.Container, error) {
	return nil, nil
}
func (b *recordingDockerBackend) ContainerStatus(context.Context, string) (*dockerconn.Container, error) {
	return &dockerconn.Container{}, nil
}
func (b *recordingDockerBackend) StartContainer(_ context.Context, name string) error {
	b.started = name
	return nil
}
func (b *recordingDockerBackend) StopContainer(context.Context, string) error   { return nil }
func (b *recordingDockerBackend) RemoveContainer(context.Context, string) error { return nil }
func (b *recordingDockerBackend) ContainerLogs(context.Context, string, int) (string, error) {
	return "", nil
}
func (b *recordingDockerBackend) ComposeUp(_ context.Context, file string) error {
	b.upFile = file
	return nil
}
func (b *recordingDockerBackend) ComposeStop(context.Context, string) error { return nil }
func (b *recordingDockerBackend) ComposeDown(context.Context, string) error { return nil }
func (b *recordingDockerBackend) ComposePS(context.Context, string) (*dockerconn.ComposeStack, error) {
	return &dockerconn.ComposeStack{}, nil
}

// The console refuses ad-hoc docker targets by name and operates a declared
// resource by id.
func TestWebDockerTakesAResourceNotAnAdHocTarget(t *testing.T) {
	backend := &recordingDockerBackend{}
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(backend))
	svc := cerbapi.NewExternalConnectorService(audit.NewMemory(), registry)
	svc.SetResourceLookup(cerbapi.ConfigResourceLookup(&config.ConfigV2{Version: 2, Resources: []config.ResourceDef{{
		ID: "mtbf-monitor", Type: "container", Connector: "docker",
		Config: map[string]any{"compose_file": "/srv/mtbf/docker-compose.yml"},
	}}}))
	srv, err := New(cerbapi.NewInProcessClient(cerbapi.WithExternalConnectorService(svc)), audit.NewMemory(), "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := signedIn(t, srv, testGuard())
	token := sessionToken(t, handler)
	post := func(body string) *httptest.ResponseRecorder {
		req := newTestRequest(http.MethodPost, "/api/connectors/docker/operations/start", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cerberus-Web-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	for _, field := range []string{
		`"host":"ssh://evil@x"`, `"context":"prod"`, `"docker_host":"tcp://x:2376"`,
		`"compose_file":"/tmp/evil.yml"`, `"composeFile":"/tmp/evil.yml"`, `"file":"/tmp/evil.yml"`,
		`"anything_else":"x"`,
	} {
		rec := post(`{"config":{"container":"web",` + field + `}}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "refusing fields") {
			t.Fatalf("%s: %d %s, want 400 refusal", field, rec.Code, rec.Body.String())
		}
	}
	if backend.started != "" || backend.upFile != "" {
		t.Fatalf("a refused request reached the backend: %+v", backend)
	}

	if rec := post(`{"config":{"resource":"mtbf-monitor"},"acknowledged":true}`); rec.Code != http.StatusOK {
		t.Fatalf("by resource: %d %s", rec.Code, rec.Body.String())
	}
	if backend.upFile != "/srv/mtbf/docker-compose.yml" {
		t.Fatalf("compose up ran %q, want the declared file", backend.upFile)
	}
}
