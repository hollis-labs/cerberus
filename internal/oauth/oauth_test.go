package oauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/hollis-labs/cerberus/internal/audit"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

type memKeys struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *memKeys) Get(_ context.Context, service, key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[service+"/"+key], nil
}

func (k *memKeys) Set(_ context.Context, service, key, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string]string{}
	}
	k.m[service+"/"+key] = value
	return nil
}

var operator = audit.Principal{Kind: "human", Via: "cli"}

func builtinIssuer(t *testing.T, resource string) (Issuer, *Verifier) {
	t.Helper()
	cfg := Config{Resource: resource, Builtin: true}
	key, err := LoadOrCreateKey(context.Background(), &memKeys{})
	if err != nil {
		t.Fatal(err)
	}
	iss := Issuer{Config: cfg, Key: key, Store: TokenStore{Dir: t.TempDir()}}
	set := iss.JWKS()
	v, err := NewVerifier(cfg, VerifierOptions{Builtin: &set, Revoked: iss.Store.Revoked})
	if err != nil {
		t.Fatal(err)
	}
	return iss, v
}

// A built-in token verifies for its resource, names its client and scopes,
// and stops verifying once revoked.
func TestBuiltinTokens(t *testing.T) {
	iss, v := builtinIssuer(t, "https://cerberus.example:4785/mcp")
	token, rec, err := iss.Issue("claude-code", []string{"cerberus:read", "cerberus:operate"}, time.Hour, operator)
	if err != nil {
		t.Fatal(err)
	}
	id, err := v.Verify(context.Background(), token)
	if err != nil || id.Client != "claude-code" || id.Subject != "client:claude-code" || id.TokenID != rec.ID || id.Issuer != "https://cerberus.example:4785" ||
		strings.Join(id.Scopes, ",") != "cerberus:read,cerberus:operate" {
		t.Fatalf("verify: %+v %v", id, err)
	}
	if _, err = iss.Store.Revoke(rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = v.Verify(context.Background(), token); !errors.Is(err, ErrInvalidToken) || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("a revoked token: %v", err)
	}
	list, _ := iss.Store.List()
	if len(list) != 1 || list[0].RevokedAt == nil || strings.Contains(mustJSON(t, list), token) {
		t.Fatalf("the store: %+v", list)
	}
}

// A token for another resource, from another issuer, expired, or signed
// with none or HMAC, does not verify.
func TestTokensThatDoNotVerify(t *testing.T) {
	iss, v := builtinIssuer(t, "https://cerberus.example:4785/mcp")
	other, _ := builtinIssuer(t, "https://other.example/mcp")
	foreign, _, _ := other.Issue("x", []string{"cerberus:read"}, time.Hour, operator)
	if _, err := v.Verify(context.Background(), foreign); err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("another issuer's token: %v", err)
	}
	// Same issuer and key, another audience: RFC 8707 binding.
	wrongAud := sign(t, iss, jwt.MapClaims{"iss": "https://cerberus.example:4785", "aud": "https://cerberus.example:4785/other", "sub": "client:x", "jti": "tok_1",
		"scope": "cerberus:read", "exp": time.Now().Add(time.Hour).Unix()})
	if _, err := v.Verify(context.Background(), wrongAud); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("another audience: %v", err)
	}
	token, _, _ := iss.Issue("x", []string{"cerberus:read"}, time.Hour, operator)
	late := *v
	late.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := late.Verify(context.Background(), token); err == nil {
		t.Fatal("an expired token verified")
	}
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"iss": "https://cerberus.example:4785", "aud": "https://cerberus.example:4785/mcp", "sub": "s",
		"exp": time.Now().Add(time.Hour).Unix()})
	unsigned, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := v.Verify(context.Background(), unsigned); err == nil {
		t.Fatal("alg none verified")
	}
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": "https://cerberus.example:4785", "aud": "https://cerberus.example:4785/mcp", "sub": "s",
		"exp": time.Now().Add(time.Hour).Unix()})
	hmac, _ := hs.SignedString([]byte("guess"))
	if _, err := v.Verify(context.Background(), hmac); err == nil {
		t.Fatal("HS256 verified")
	}
	// No scope of ours.
	noScope := sign(t, iss, jwt.MapClaims{"iss": "https://cerberus.example:4785", "aud": "https://cerberus.example:4785/mcp", "sub": "client:x", "jti": "tok_2",
		"scope": "openid", "exp": time.Now().Add(time.Hour).Unix()})
	if _, err := v.Verify(context.Background(), noScope); err == nil {
		t.Fatal("a token with none of our scopes verified")
	}
}

func sign(t *testing.T, iss Issuer, c jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tok.Header["kid"] = iss.JWKS().Keys[0].Kid
	s, err := tok.SignedString(iss.Key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// An external issuer's keys are discovered from its metadata, and its
// tokens verify with the client and scopes from their claims.
func TestExternalIssuer(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	var fetches int
	var mu sync.Mutex
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "jwks_uri": srv.URL + "/jwks"})
		case "/jwks":
			mu.Lock()
			fetches++
			mu.Unlock()
			b := func(n []byte) string { return base64.RawURLEncoding.EncodeToString(n) }
			_ = json.NewEncoder(w).Encode(JWKS{Keys: []JWK{{Kty: "EC", Crv: "P-256", Kid: "k1", X: b(key.X.FillBytes(make([]byte, 32))), Y: b(key.Y.FillBytes(make([]byte, 32)))}}})
		default:
			http.NotFound(w, r)
		}
	})
	cfg := Config{Resource: "https://cerberus.example/mcp", Issuers: []IssuerConfig{{Issuer: srv.URL, ClientClaim: "azp", ScopesClaim: "scp"}}}
	v, err := NewVerifier(cfg, VerifierOptions{HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	mint := func(kid string) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": srv.URL, "aud": []string{"https://cerberus.example/mcp/"}, "sub": "user-42", "azp": "https://client.example/cimd.json",
			"scp": []string{"cerberus:read_sensitive"}, "exp": time.Now().Add(time.Hour).Unix()})
		tok.Header["kid"] = kid
		s, _ := tok.SignedString(key)
		return s
	}
	id, err := v.Verify(context.Background(), mint("k1"))
	if err != nil || id.Subject != "user-42" || id.Client != "https://client.example/cimd.json" || id.Scopes[0] != "cerberus:read_sensitive" {
		t.Fatalf("external: %+v %v", id, err)
	}
	// An unknown kid refetches once, then not again within the minute.
	for i := 0; i < 5; i++ {
		_, _ = v.Verify(context.Background(), mint("nope"))
	}
	mu.Lock()
	defer mu.Unlock()
	if fetches > 2 {
		t.Fatalf("%d JWKS fetches for a stranger's kid", fetches)
	}
}

func TestScopes(t *testing.T) {
	for _, c := range []struct {
		scopes []string
		effect contract.Effect
		ok     bool
	}{
		{[]string{ScopeRead}, contract.EffectRead, true},
		{[]string{ScopeRead}, contract.EffectReadSensitive, false},
		{[]string{ScopeReadSensitive}, contract.EffectReadSensitive, true},
		{[]string{ScopeReadSensitive}, contract.EffectWrite, false},
		{[]string{ScopeOperate}, contract.EffectDestructive, true},
		{[]string{ScopeOperate}, contract.EffectRead, true},
		{nil, contract.EffectRead, false},
	} {
		if Allows(c.scopes, c.effect) != c.ok {
			t.Errorf("%v %s: want %v", c.scopes, c.effect, c.ok)
		}
	}
}

func TestIssueRefusals(t *testing.T) {
	iss, _ := builtinIssuer(t, "https://cerberus.example/mcp")
	if _, _, err := iss.Issue("x", []string{"cerberus:admin"}, time.Hour, operator); err == nil || !strings.Contains(err.Error(), "unknown scopes") {
		t.Fatalf("unknown scope: %v", err)
	}
	if _, _, err := iss.Issue("x", []string{"cerberus:read"}, 100*24*time.Hour, operator); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("over the max ttl: %v", err)
	}
	if _, _, err := iss.Issue("", []string{"cerberus:read"}, time.Hour, operator); err == nil {
		t.Fatal("no client")
	}
}

func TestConfigProblems(t *testing.T) {
	for want, c := range map[string]Config{
		"resource is required":    {Builtin: true},
		"plain http off loopback": {Builtin: true, Resource: "http://cerberus.example:4785/mcp"},
		"must be an https URL":    {Resource: "https://c.example/mcp", Issuers: []IssuerConfig{{Issuer: "http://idp.example"}}},
		"both cert and key":       {Builtin: true, Resource: "https://c.example/mcp", TLS: TLSConfig{Cert: "c.pem"}},
	} {
		if p := strings.Join(c.Problems(), "; "); !strings.Contains(p, want) {
			t.Errorf("want %q in %q", want, p)
		}
	}
	if p := (Config{Builtin: true, Resource: "http://127.0.0.1:4785/mcp"}).Problems(); len(p) != 0 {
		t.Fatalf("loopback http: %v", p)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
