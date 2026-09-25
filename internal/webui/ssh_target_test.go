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
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
)

type recordingSSHBackend struct {
	connects int
	host     string
	command  string
}

func (b *recordingSSHBackend) Connect(_ context.Context, host string, _ int, _ string, _ string, _ sshconn.HostKeyConfig) error {
	b.connects++
	b.host = host
	return nil
}
func (b *recordingSSHBackend) Exec(_ context.Context, command string) (*sshconn.ExecResult, error) {
	b.command = command
	return &sshconn.ExecResult{Stdout: "ok"}, nil
}
func (b *recordingSSHBackend) Ping(context.Context) error { return nil }
func (b *recordingSSHBackend) Put(context.Context, string, string) (int64, error) {
	return 0, nil
}
func (b *recordingSSHBackend) Get(context.Context, string, string) (int64, error) {
	return 0, nil
}
func (b *recordingSSHBackend) PutDir(context.Context, string, string) (*sshconn.DirTransferResult, error) {
	return &sshconn.DirTransferResult{}, nil
}
func (b *recordingSSHBackend) GetDir(context.Context, string, string) (*sshconn.DirTransferResult, error) {
	return &sshconn.DirTransferResult{}, nil
}
func (b *recordingSSHBackend) Close() error { return nil }

// The web console reaches SSH through the same service as the socket, so it
// gets the same rule: a configured resource id, and no connection fields.
func TestWebSSHOperationsTakeOnlyAResourceID(t *testing.T) {
	backend := &recordingSSHBackend{}
	registry := connector.NewRegistry()
	registry.Register(sshconn.NewWithBackendFactory(nil, func() sshconn.Backend { return backend }))
	svc := cerbapi.NewExternalConnectorService(audit.NewMemory(), registry)
	svc.SetResourceLookup(cerbapi.ConfigResourceLookup(&config.ConfigV2{Version: 2, Resources: []config.ResourceDef{{
		ID: "server-1", Connector: "ssh", Type: "server",
		Config: map[string]any{"host": "10.0.0.9", "user": "ops", "key_file": "/tmp/k", "allow_insecure_host_key": true},
	}}}))
	srv, err := New(cerbapi.NewInProcessClient(cerbapi.WithExternalConnectorService(svc)), "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler(testGuard())
	token := sessionToken(t, handler)

	post := func(body string) *httptest.ResponseRecorder {
		req := newTestRequest(http.MethodPost, "/api/connectors/ssh/operations/exec", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cerberus-Web-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	for _, override := range []string{
		`"host":"evil.example"`,
		`"key_file":"/Users/me/.ssh/id_ed25519"`,
		`"allow_insecure_host_key":true`,
	} {
		rec := post(`{"acknowledged":true,"config":{"id":"server-1","command":"uptime",` + override + `}}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "refusing fields") || !strings.Contains(rec.Body.String(), "configured resource id") {
			t.Fatalf("override %s: %d %s, want 400 refusal", override, rec.Code, rec.Body.String())
		}
	}
	if backend.connects != 0 {
		t.Fatal("a refused request connected")
	}

	rec := post(`{"acknowledged":true,"config":{"id":"server-1","command":"uptime"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("by id: %d %s", rec.Code, rec.Body.String())
	}
	if backend.host != "10.0.0.9" || backend.command != "uptime" {
		t.Fatalf("backend = %+v, want the configured host and the command", backend)
	}
}
