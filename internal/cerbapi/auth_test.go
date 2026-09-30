package cerbapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/oauth"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

type memKeyStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *memKeyStore) Get(_ context.Context, s, key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[s+"/"+key], nil
}

func (k *memKeyStore) Set(_ context.Context, s, key, v string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string]string{}
	}
	k.m[s+"/"+key] = v
	return nil
}

const testResource = "https://cerberus.example:4785/mcp"

// withAuth installs a daemon auth with the built-in issuer.
func withAuth(t *testing.T, sink audit.Sink) *Auth {
	t.Helper()
	cfg := oauth.Config{Resource: testResource, Builtin: true}
	key, err := oauth.LoadOrCreateKey(context.Background(), &memKeyStore{})
	if err != nil {
		t.Fatal(err)
	}
	iss := &oauth.Issuer{Config: cfg, Key: key, Store: oauth.TokenStore{Dir: t.TempDir()}}
	set := iss.JWKS()
	v, err := oauth.NewVerifier(cfg, oauth.VerifierOptions{Builtin: &set, Revoked: iss.Store.Revoked})
	if err != nil {
		t.Fatal(err)
	}
	a := &Auth{Config: cfg, Verifier: v, Issuer: iss, Sink: sink}
	SetAuth(a)
	t.Cleanup(func() { SetAuth(nil) })
	return a
}

func mint(t *testing.T, a *Auth, client string, scopes ...string) (string, oauth.TokenRecord) {
	t.Helper()
	tok, rec, err := a.Issuer.Issue(client, scopes, time.Hour, audit.Principal{Kind: "human", Via: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	return tok, rec
}

func bearerRequest(token string, claim Principal) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/ping", nil)
	if token != "" {
		r.Header.Set(BearerHeader, token)
	}
	setPrincipalHeader(r.Header, claim)
	_, r = BeginHTTPRequest(httptest.NewRecorder(), r, SurfaceSocket)
	return r
}

// A forwarded token that verifies gives the call a verified principal: an
// agent over mcp_http, whatever the claim said. One that does not verify
// refuses the request. None leaves the claim as it was.
func TestForwardedBearerIsVerifiedByTheDaemon(t *testing.T) {
	a := withAuth(t, audit.NewMemory())
	tok, rec := mint(t, a, "claude-code", "cerberus:read")
	r, err := verifyBearer(bearerRequest(tok, Principal{Kind: PrincipalHuman, Via: ViaCLI, Client: "pretender"}))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := PrincipalFrom(r.Context())
	if p.Kind != PrincipalAgent || p.Via != ViaMCPHTTP || !p.Verified() || p.Subject != "client:claude-code" || p.Client != "claude-code" ||
		p.TokenID != rec.ID || p.Issuer != "https://cerberus.example:4785" || strings.Join(p.Scopes, ",") != "cerberus:read" {
		t.Fatalf("verified principal: %+v", p)
	}
	if _, err = verifyBearer(bearerRequest(tok+"x", Principal{Kind: PrincipalAgent, Via: ViaMCPHTTP})); err == nil {
		t.Fatal("a damaged token was accepted")
	}
	r, err = verifyBearer(bearerRequest("", Principal{Kind: PrincipalAgent, Via: ViaMCPHTTP, Client: "c"}))
	if p, _ = PrincipalFrom(r.Context()); err != nil || p.Verified() || !p.SelfReported {
		t.Fatalf("no token: %+v %v", p, err)
	}
	SetAuth(nil)
	if _, err = verifyBearer(bearerRequest(tok, Principal{Kind: PrincipalAgent, Via: ViaMCPHTTP})); err == nil || !strings.Contains(err.Error(), "no mcp-http auth configured") {
		t.Fatalf("a daemon with no auth: %v", err)
	}
}

// A verified caller's scopes narrow what it may run, before policy and in
// every mode; its audit record names the token, never its text.
func TestScopesNarrowAVerifiedCaller(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Allow})
	sink := audit.NewMemory()
	a := withAuth(t, sink)
	svc, backend := dockerLane(t, sink)
	readTok, _ := mint(t, a, "reader", "cerberus:read")
	r, err := verifyBearer(bearerRequest(readTok, Principal{Kind: PrincipalAgent, Via: ViaMCPHTTP}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Execute(r.Context(), devStop())
	if connectorErrorCode(err) != ExternalConnectorInsufficientScope || !strings.Contains(err.Error(), "needs cerberus:operate") ||
		!strings.Contains(err.Error(), "cerberus mcp-http token issue --client reader --scope cerberus:operate") || backend.stopped != "" {
		t.Fatalf("a read token stopping a container: %v", err)
	}
	if redact.Text(err.Error()) != err.Error() {
		t.Fatalf("redaction rewrote the refusal: %q", redact.Text(err.Error()))
	}
	opTok, rec := mint(t, a, "operator-bot", "cerberus:operate")
	r, _ = verifyBearer(bearerRequest(opTok, Principal{Kind: PrincipalAgent, Via: ViaMCPHTTP}))
	if _, err = svc.Execute(r.Context(), devStop()); err != nil || backend.stopped == "" {
		t.Fatalf("an operate token: %v", err)
	}
	o := outcome(sink.Records())
	if o.Principal.AuthMethod != AuthOAuth || o.Principal.TokenID != rec.ID || o.Principal.Subject != "client:operator-bot" || o.Principal.SelfReported {
		t.Fatalf("audit principal: %+v", o.Principal)
	}
	for _, rec := range sink.Records() {
		data, _ := json.Marshal(rec)
		if strings.Contains(string(data), opTok) || strings.Contains(string(data), readTok) {
			t.Fatal("a token reached the audit log")
		}
	}
	// A verified caller is keyed by its subject, across sessions.
	k1 := callerKey(audit.Principal{Kind: "agent", Via: "mcp_http", AuthMethod: AuthOAuth, Issuer: "i", Subject: "s", Client: "c", Session: "one"})
	k2 := callerKey(audit.Principal{Kind: "agent", Via: "mcp_http", AuthMethod: AuthOAuth, Issuer: "i", Subject: "s", Client: "c", Session: "two"})
	if k1 != k2 || !strings.Contains(k1, "oauth:i#s") {
		t.Fatalf("keys %q %q", k1, k2)
	}
}

// Minting and revoking are a person's, at the CLI, with the phrase typed;
// capabilities say what the daemon verifies.
func TestTokenRoutes(t *testing.T) {
	sink := audit.NewMemory()
	a := withAuth(t, sink)
	s := &SocketServer{}
	call := func(p Principal, method, path string, body any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(string(data))).WithContext(as(p))
		w, r := BeginHTTPRequest(rec, req, SurfaceSocket)
		s.handleAuth(w, r.WithContext(WithPrincipal(r.Context(), p)))
		return rec
	}
	rec := call(humanCLI, http.MethodGet, "/auth/capabilities", nil)
	var caps AuthCapabilities
	if json.Unmarshal(rec.Body.Bytes(), &caps) != nil || !caps.OAuth || caps.Resource != testResource || caps.BuiltinJWKS == nil || len(caps.BuiltinJWKS.Keys) != 1 {
		t.Fatalf("capabilities: %s", rec.Body.String())
	}
	if rec = call(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, http.MethodPost, "/auth/tokens", TokenIssueArgs{Client: "x", Scopes: []string{"cerberus:read"}, Typed: "issue x"}); rec.Code == http.StatusOK {
		t.Fatal("an agent minted a token")
	}
	if rec = call(humanCLI, http.MethodPost, "/auth/tokens", TokenIssueArgs{Client: "x", Scopes: []string{"cerberus:read"}, Typed: "yes"}); rec.Code == http.StatusOK {
		t.Fatal("a wrong phrase minted a token")
	}
	rec = call(humanCLI, http.MethodPost, "/auth/tokens", TokenIssueArgs{Client: "x", Scopes: []string{"cerberus:read"}, Typed: "issue x"})
	var issued IssuedToken
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &issued) != nil || issued.Token == "" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("issue: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := a.Verifier.Verify(context.Background(), issued.Token); err != nil {
		t.Fatal(err)
	}
	if rec = call(humanCLI, http.MethodGet, "/auth/tokens", nil); strings.Contains(rec.Body.String(), issued.Token) || !strings.Contains(rec.Body.String(), issued.Record.ID) {
		t.Fatalf("list: %s", rec.Body.String())
	}
	if rec = call(humanCLI, http.MethodPost, "/auth/tokens/"+issued.Record.ID+"/revoke", TokenRevokeArgs{Typed: "revoke " + issued.Record.ID}); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := a.Verifier.Verify(context.Background(), issued.Token); err == nil {
		t.Fatal("a revoked token verified")
	}
	for _, r := range sink.Records() {
		data, _ := json.Marshal(r)
		if strings.Contains(string(data), issued.Token) {
			t.Fatal("the token reached the audit log")
		}
	}
	SetAuth(nil)
	if rec = call(humanCLI, http.MethodGet, "/auth/capabilities", nil); !strings.Contains(rec.Body.String(), `"oauth":false`) {
		t.Fatalf("no auth: %s", rec.Body.String())
	}
}
