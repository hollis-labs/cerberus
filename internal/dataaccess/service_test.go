package dataaccess

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/pkg/plugin"
	auth "github.com/hollis-labs/libs/security/auth"
)

type fixture struct {
	s      *Service
	grant  Grant
	q      Request
	claims map[string]any
	key    ed25519.PrivateKey
	events []Event
	mu     sync.Mutex
	calls  atomic.Int32
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "OKP", "crv": "Ed25519", "use": "sig", "kid": "test", "x": base64.RawURLEncoding.EncodeToString(pub)}}})
	}))
	t.Cleanup(srv.Close)
	v, err := auth.NewJWTVerifier(auth.JWTConfig{Issuer: "https://tether.test", Audience: Audience, JWKSURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	f := &fixture{key: key, claims: map[string]any{"iss": "https://tether.test", "sub": "agent-a", "aud": Audience, "scope": Scope, "kind": "agent", "sid": "session-a", "jti": "token-a", "par": "parent-a", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}}
	f.grant = Grant{ID: "grant-a", Issuer: "https://tether.test", Subject: "agent-a", Session: "session-a", TokenID: "token-a", Parent: "parent-a", Connector: "postgres-data", Target: "fixture", Projection: "documents", Version: "v1", ExpiresAt: now.Add(time.Minute), MaxRows: 100, MaxBytes: 1 << 20}
	f.q = Request{GrantID: "grant-a", DataQuery: plugin.DataQuery{Target: "fixture", Projection: "documents", Version: "v1", Parameters: json.RawMessage(`{"term":"private query value"}`)}}
	rev := NewRevocations("")
	rev.last = now
	f.s = &Service{Issuer: "https://tether.test", Verifier: v, Revocations: rev, Grants: func() ([]Grant, error) { return []Grant{f.grant}, nil }, Authorize: func(context.Context, auth.Principal, Grant) error { return nil }, Audit: func(e Event) error { f.mu.Lock(); defer f.mu.Unlock(); f.events = append(f.events, e); return nil }}
	f.s.Execute = func(ctx context.Context, p auth.Principal, g Grant, q plugin.DataQuery) (Result, error) {
		f.calls.Add(1)
		proof, ok := FromContext(ctx)
		if !ok || proof.Permit.Validate(q, time.Now()) != nil {
			t.Error("missing bound internal proof")
		}
		return Result{Columns: []string{"id"}, Rows: []json.RawMessage{json.RawMessage(`[1]`)}}, nil
	}
	return f
}
func (f *fixture) token() string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	s := enc(map[string]string{"alg": "EdDSA", "kid": "test"}) + "." + enc(f.claims)
	return s + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.key, []byte(s)))
}
func TestVerifiedSessionProjectionAndCompletion(t *testing.T) {
	f := newFixture(t)
	out, err := f.s.Query(context.Background(), f.token(), f.q)
	if err != nil || len(out.Rows) != 1 {
		t.Fatalf("query: %v", err)
	}
	if len(f.events) != 2 || f.events[0].Kind != "intent" || f.events[1].Outcome != "ok" || f.events[0].ID != f.events[1].ID {
		t.Fatal("audit pair missing")
	}
}
func TestRefuseIdentityScopeAndGrantConfusion(t *testing.T) {
	cases := map[string]func(*fixture){
		"wrong audience": func(f *fixture) { f.claims["aud"] = "cerberus" }, "operator": func(f *fixture) { f.claims["kind"] = "operator" }, "wildcard": func(f *fixture) { f.claims["scope"] = "*" }, "missing parent": func(f *fixture) { delete(f.claims, "par") }, "changed parent": func(f *fixture) { f.claims["par"] = "other" }, "changed session": func(f *fixture) { f.claims["sid"] = "other" }, "changed token": func(f *fixture) { f.claims["jti"] = "other" }, "changed subject": func(f *fixture) { f.claims["sub"] = "other" }, "expired within leeway": func(f *fixture) {
			f.claims["exp"] = time.Now().Add(-time.Second).Unix()
			f.claims["iat"] = time.Now().Add(-time.Minute).Unix()
		}, "future iat": func(f *fixture) { f.claims["iat"] = time.Now().Add(time.Second).Unix() }, "long lease": func(f *fixture) { f.claims["exp"] = time.Now().Add(10 * time.Minute).Unix() }, "wrong projection": func(f *fixture) { f.q.Projection = "other" }, "wrong version": func(f *fixture) { f.q.Version = "v2" }, "wrong target": func(f *fixture) { f.q.Target = "other" }, "no grant": func(f *fixture) { f.q.GrantID = "none" }, "expired grant": func(f *fixture) { f.grant.ExpiresAt = time.Now().Add(-time.Second) }, "stale revocations": func(f *fixture) { f.s.Revocations.last = time.Now().Add(-11 * time.Second) }, "policy deny": func(f *fixture) {
			f.s.Authorize = func(context.Context, auth.Principal, Grant) error { return errors.New("deny") }
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			change(f)
			out, err := f.s.Query(context.Background(), f.token(), f.q)
			if err == nil || len(out.Rows) != 0 || f.calls.Load() != 0 {
				t.Fatal("invalid authority reached executor")
			}
			if len(f.events) == 0 {
				t.Fatal("denial not audited")
			}
		})
	}
}
func TestNoDeliveryAfterCompletionAuditFailureOrExpiry(t *testing.T) {
	for _, kind := range []string{"intent fails", "completion fails", "completion expires", "rows exceed", "bytes exceed"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.s.Audit = func(e Event) error {
				if kind == "intent fails" || kind == "completion fails" && e.Kind == "outcome" {
					return errors.New("disk failed")
				}
				if kind == "completion expires" && e.Kind == "outcome" {
					f.grant.ExpiresAt = time.Now().Add(-time.Second)
				}
				return nil
			}
			if kind == "rows exceed" {
				f.s.Execute = func(context.Context, auth.Principal, Grant, plugin.DataQuery) (Result, error) {
					rows := make([]json.RawMessage, 101)
					for i := range rows {
						rows[i] = json.RawMessage(`[]`)
					}
					return Result{Rows: rows}, nil
				}
			}
			if kind == "bytes exceed" {
				f.grant.MaxBytes = 2
			}
			out, err := f.s.Query(context.Background(), f.token(), f.q)
			if err == nil || len(out.Rows) != 0 {
				t.Fatal("released unauthorized output")
			}
			if kind == "intent fails" && f.calls.Load() != 0 {
				t.Fatal("executed without durable admission")
			}
		})
	}
}
func TestRevocationCancelsInFlightAndSessionConcurrency(t *testing.T) {
	f := newFixture(t)
	entered := make(chan struct{}, 2)
	f.s.Execute = func(ctx context.Context, _ auth.Principal, _ Grant, _ plugin.DataQuery) (Result, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return Result{Rows: []json.RawMessage{json.RawMessage(`[1]`)}}, nil
	}
	raw := f.token()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := f.s.Query(context.Background(), raw, f.q); done <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("executor not entered")
		}
	}
	if _, err := f.s.Query(context.Background(), raw, f.q); err == nil {
		t.Fatal("third session request admitted")
	}
	f.s.Revocations.List.Replace(auth.DenyListDoc{SID: []string{"session-a"}})
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("revoked response released")
			}
		case <-time.After(time.Second):
			t.Fatal("revocation did not cancel")
		}
	}
	if f.s.total != 0 || len(f.s.active) != 0 {
		t.Fatal("concurrency slot leaked")
	}
}
func TestRevocationSnapshotFreshnessAnd304(t *testing.T) {
	mode := "304"
	now := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "304":
			w.WriteHeader(304)
		case "bad":
			_, _ = w.Write([]byte(`{}`))
		case "fail":
			w.WriteHeader(500)
		default:
			w.Header().Set("ETag", "v1")
			_, _ = w.Write([]byte(`{"jti":["blocked"],"sub":[],"sid":[]}`))
		}
	}))
	defer srv.Close()
	r := NewRevocations(srv.URL)
	r.Now = func() time.Time { return now }
	if r.Poll(context.Background()) == nil {
		t.Fatal("304 before snapshot accepted")
	}
	mode = "full"
	if r.Poll(context.Background()) != nil {
		t.Fatal("full snapshot failed")
	}
	now = now.Add(9 * time.Second)
	mode = "304"
	if r.Poll(context.Background()) != nil {
		t.Fatal("valid 304 failed")
	}
	if r.Check(auth.Principal{TokenID: "blocked"}) == nil {
		t.Fatal("304 cleared revocation")
	}
	mode = "bad"
	if r.Poll(context.Background()) == nil {
		t.Fatal("empty snapshot accepted")
	}
	mode = "fail"
	_ = r.Poll(context.Background())
	now = now.Add(10 * time.Second)
	if r.Check(auth.Principal{}) == nil {
		t.Fatal("failed refresh advanced freshness")
	}
}
