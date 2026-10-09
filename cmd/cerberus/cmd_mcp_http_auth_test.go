package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/loopback"
	"github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/oauth"
	gmcp "github.com/hollis-labs/libs/plugin-mcp/go-mcp/server"
)

type memKS struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *memKS) Get(_ context.Context, s, key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[s+"/"+key], nil
}

func (k *memKS) Set(_ context.Context, s, key, v string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string]string{}
	}
	k.m[s+"/"+key] = v
	return nil
}

const loopResource = "http://127.0.0.1:4785/mcp"

type fakeCaps struct {
	caps cerbapi.AuthCapabilities
	err  error
}

func (f fakeCaps) AuthCapabilities(context.Context) (cerbapi.AuthCapabilities, error) {
	return f.caps, f.err
}

// withAuthConfig makes mcp-http.yaml say cfg, and returns an issuer for it
// and the capabilities a daemon with that config answers.
func withAuthConfig(t *testing.T, cfg oauth.Config) (*oauth.Issuer, cerbapi.AuthCapabilities) {
	t.Helper()
	old := mcpHTTPAuthConfig
	mcpHTTPAuthConfig = func() (oauth.Config, error) { return cfg, nil }
	t.Cleanup(func() { mcpHTTPAuthConfig = old })
	key, err := oauth.LoadOrCreateKey(context.Background(), &memKS{})
	if err != nil {
		t.Fatal(err)
	}
	iss := &oauth.Issuer{Config: cfg, Key: key, Store: oauth.TokenStore{Dir: t.TempDir()}}
	set := iss.JWKS()
	return iss, cerbapi.AuthCapabilities{OAuth: true, Resource: cfg.Resource, Builtin: true, BuiltinIssuer: cfg.BuiltinIssuer(), BuiltinJWKS: &set}
}

// mcp-http refuses to require auth from a daemon that would not verify it.
func TestMCPHTTPAuthNeedsTheDaemon(t *testing.T) {
	cfg := oauth.Config{Resource: loopResource, Builtin: true}
	_, caps := withAuthConfig(t, cfg)
	for want, client := range map[string]fakeCaps{
		"an older daemon does not":   {err: errors.New("404 page not found")},
		"the daemon has none":        {caps: cerbapi.AuthCapabilities{}},
		"the daemon verifies tokens": {caps: cerbapi.AuthCapabilities{OAuth: true, Resource: "https://other.example/mcp"}},
	} {
		if _, err := setupMCPHTTPAuth(context.Background(), client); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q: %v", want, err)
		}
	}
	a, err := setupMCPHTTPAuth(context.Background(), fakeCaps{caps: caps})
	if err != nil || a == nil {
		t.Fatalf("a daemon that verifies: %v", err)
	}
	mcpHTTPAuthConfig = func() (oauth.Config, error) { return oauth.Config{}, oauth.ErrNotConfigured }
	if a, err = setupMCPHTTPAuth(context.Background(), fakeCaps{err: errors.New("unused")}); a != nil || err != nil {
		t.Fatalf("no config: %v %v", a, err)
	}
}

// bearerClient records the bearer token each call's context carried.
type bearerClient struct {
	*cerbapi.InProcessClient
	mu   sync.Mutex
	seen []string
}

func (c *bearerClient) ListProjects(ctx context.Context) ([]cerbapi.ProjectInfo, error) {
	c.mu.Lock()
	c.seen = append(c.seen, mcp.BearerFromContext(ctx))
	c.mu.Unlock()
	return c.InProcessClient.ListProjects(ctx)
}

// With auth on, /mcp needs a bearer token bound to this resource, the
// metadata says where tokens come from, a call over the token's scope is
// refused in text the model reads, and the token rides on to the daemon.
func TestMCPHTTPResourceServer(t *testing.T) {
	cfg := oauth.Config{Resource: loopResource, Builtin: true}
	iss, caps := withAuthConfig(t, cfg)
	auth, err := setupMCPHTTPAuth(context.Background(), fakeCaps{caps: caps})
	if err != nil {
		t.Fatal(err)
	}
	client := &bearerClient{InProcessClient: cerbapi.NewInProcessClient(cerbapi.WithConfigV2(&config.ConfigV2{Version: 2}), cerbapi.WithInProcessAudit(audit.NewMemory()))}
	srv := buildCerberusMCPServer(client, gmcp.WithReceivingMiddleware(mcp.ScopeMiddleware))
	h := mcpHTTPHandler(srv, "/mcp", loopback.NewGuard("127.0.0.1", "4785"), auth)

	post := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Host = "127.0.0.1:4785"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-03-26")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1:4785"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	const list = `{"jsonrpc":"2.0","id":"l1","method":"tools/list","params":{}}`

	rec := post("", list)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Header().Get("WWW-Authenticate"), `resource_metadata="http://127.0.0.1:4785/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("no token: %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	var prm map[string]any
	if rec = get("/.well-known/oauth-protected-resource/mcp"); json.Unmarshal(rec.Body.Bytes(), &prm) != nil || prm["resource"] != loopResource ||
		!strings.Contains(rec.Body.String(), `"authorization_servers":["http://127.0.0.1:4785"]`) || !strings.Contains(rec.Body.String(), "cerberus:operate") {
		t.Fatalf("protected resource metadata: %s", rec.Body.String())
	}
	if rec = get("/.well-known/jwks.json"); !strings.Contains(rec.Body.String(), `"kty":"OKP"`) {
		t.Fatalf("jwks: %s", rec.Body.String())
	}
	if rec = get("/.well-known/oauth-authorization-server"); !strings.Contains(rec.Body.String(), `"jwks_uri":"http://127.0.0.1:4785/.well-known/jwks.json"`) {
		t.Fatalf("issuer metadata: %s", rec.Body.String())
	}

	// A token minted for another resource is refused: RFC 8707.
	otherCfg := oauth.Config{Resource: "http://127.0.0.1:9999/mcp", Builtin: true}
	other := oauth.Issuer{Config: otherCfg, Key: iss.Key, Store: oauth.TokenStore{Dir: t.TempDir()}}
	foreign, _, _ := other.Issue("x", []string{"cerberus:operate"}, time.Hour, audit.Principal{})
	if rec = post(foreign, list); rec.Code != http.StatusUnauthorized {
		t.Fatalf("another resource's token: %d", rec.Code)
	}

	readTok, _, _ := iss.Issue("reader", []string{"cerberus:read"}, time.Hour, audit.Principal{})
	if rec = post(readTok, list); rec.Code != http.StatusOK {
		t.Fatalf("a valid token listing tools: %d %s", rec.Code, rec.Body.String())
	}
	rec = post(readTok, `{"jsonrpc":"2.0","id":"r1","method":"tools/call","params":{"name":"cerberus_pipeline_run","arguments":{"pipeline_id":"p","acknowledged":true}}}`)
	body := rec.Body.String()
	if !strings.Contains(body, "insufficient_scope") || !strings.Contains(body, "needs cerberus:operate") || !strings.Contains(body, "next_step") || !strings.Contains(body, `"isError":true`) {
		t.Fatalf("a read token running a pipeline: %s", body)
	}
	rec = post(readTok, `{"jsonrpc":"2.0","id":"r2","method":"tools/call","params":{"name":"cerberus_project_list","arguments":{}}}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "insufficient_scope") {
		t.Fatalf("a read token listing projects: %d %s", rec.Code, rec.Body.String())
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.seen) != 1 || client.seen[0] != readTok {
		t.Fatalf("the token was not carried to the daemon client: %d calls", len(client.seen))
	}
	if rec = get("/health"); rec.Code != http.StatusOK {
		t.Fatalf("health: %d", rec.Code)
	}
}
