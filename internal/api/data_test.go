package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/dataaccess"
	"github.com/hollis-labs/cerberus/pkg/plugin"
)

// dataFixture is a data service with a real session-token verifier (a JWKS and a deny-list
// served locally), one grant, and the lane's checks as the test sets them.
type dataFixture struct {
	svc   *dataaccess.Service
	key   ed25519.PrivateKey
	grant dataaccess.Grant
	runs  int
	deny  bool
	audit []dataaccess.Event
}

func newDataFixture(t *testing.T) *dataFixture {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/deny" {
			_, _ = w.Write([]byte(`{"jti":[],"sub":[],"sid":[]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "OKP", "crv": "Ed25519", "use": "sig", "kid": "test", "x": base64.RawURLEncoding.EncodeToString(pub)}}})
	}))
	t.Cleanup(srv.Close)
	rev := dataaccess.NewRevocations(srv.URL + "/deny")
	if err := rev.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, err := libauth.NewJWTVerifier(libauth.JWTConfig{Issuer: "https://tether.test", Audience: dataaccess.Audience, JWKSURL: srv.URL, DenyList: rev.List})
	if err != nil {
		t.Fatal(err)
	}
	f := &dataFixture{key: key}
	f.grant = dataaccess.Grant{ID: "grant-a", Issuer: "https://tether.test", Subject: "agent-a", Session: "session-a", TokenID: "token-a", Parent: "parent-a",
		Connector: "postgres-data", Target: "fixture", Projection: "documents", Version: "v1", ExpiresAt: time.Now().Add(time.Minute), MaxRows: 100, MaxBytes: 1 << 20}
	f.svc = &dataaccess.Service{Issuer: "https://tether.test", Verifier: v, Revocations: rev,
		Grants: func() ([]dataaccess.Grant, error) { return []dataaccess.Grant{f.grant}, nil },
		Authorize: func(context.Context, libauth.Principal, dataaccess.Grant) error {
			if f.deny {
				return dataaccess.ErrDenied
			}
			return nil
		},
		Execute: func(context.Context, libauth.Principal, dataaccess.Grant, plugin.DataQuery) (dataaccess.Result, error) {
			f.runs++
			return dataaccess.Result{Columns: []string{"id"}, Rows: []json.RawMessage{json.RawMessage(`[1]`)}}, nil
		},
		Audit: func(e dataaccess.Event) error { f.audit = append(f.audit, e); return nil }}
	return f
}

// token is a five-minute agent session token for the grant's subject.
func (f *dataFixture) token() string {
	now := time.Now()
	claims := map[string]any{"iss": "https://tether.test", "sub": "agent-a", "aud": dataaccess.Audience, "scope": dataaccess.Scope, "kind": "agent",
		"sid": "session-a", "jti": "token-a", "par": "parent-a", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	s := enc(map[string]string{"alg": "EdDSA", "kid": "test"}) + "." + enc(claims)
	return s + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.key, []byte(s)))
}

const dataRequest = `{"grant_id":"grant-a","target":"fixture","projection":"documents","version":"v1","parameters":{"tenant":"a"}}`

// POST /api/data/v1/query serves old's data route under /api: the session token in its own
// header beside the caller's key (never the Authorization the guard reads), a strict request, the
// version header on every answer, and the service's verdicts: a verified session with its grant
// and an allowing policy gets the rows, with the audit pair; a policy that does not allow is 403
// and runs nothing; no token is 401, a browser Origin 403, a loose body 400, and an unconfigured
// server 503.
func TestDataQueryRoute(t *testing.T) {
	f := newDataFixture(t)
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir(), Data: f.svc})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Mux()
	call := func(body string, header map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/data/v1/query", strings.NewReader(body))
		req = req.WithContext(libauth.WithPrincipal(req.Context(), apiKey))
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := call(dataRequest, map[string]string{DataTokenHeader: f.token()})
	if rec.Code != http.StatusOK || rec.Header().Get(DataVersionHeader) != "1" || rec.Body.String() != `{"columns":["id"],"rows":[[1]]}` || f.runs != 1 || len(f.audit) != 2 {
		t.Fatalf("an authorized query: %d %s runs %d audit %d", rec.Code, rec.Body, f.runs, len(f.audit))
	}
	f.deny = true
	if rec = call(dataRequest, map[string]string{DataTokenHeader: f.token()}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "data_refused") || f.runs != 1 {
		t.Fatalf("a query policy does not allow: %d %s", rec.Code, rec.Body)
	}
	f.deny = false
	for name, c := range map[string]struct {
		body   string
		header map[string]string
		code   int
	}{
		"no token":         {dataRequest, nil, http.StatusUnauthorized},
		"a bearer only":    {dataRequest, map[string]string{"Authorization": "Bearer " + f.token()}, http.StatusUnauthorized},
		"a browser origin": {dataRequest, map[string]string{DataTokenHeader: f.token(), "Origin": "https://console.test"}, http.StatusForbidden},
		"an unknown field": {strings.Replace(dataRequest, `"grant_id"`, `"sql":"x","grant_id"`, 1), map[string]string{DataTokenHeader: f.token()}, http.StatusBadRequest},
		"a forged token":   {dataRequest, map[string]string{DataTokenHeader: f.token() + "x"}, http.StatusForbidden},
	} {
		if rec = call(c.body, c.header); rec.Code != c.code || rec.Header().Get(DataVersionHeader) != "1" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if f.runs != 1 {
		t.Fatalf("a refused query ran: %d", f.runs)
	}
	off, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	g := &gatedAPI{h: off.Mux()}
	code, body := g.call(t, apiKey, "POST", "/api/data/v1/query", dataRequest)
	wantCode(t, "unconfigured", code, body, http.StatusServiceUnavailable, "data_unavailable")
}
