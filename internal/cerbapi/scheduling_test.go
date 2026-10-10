package cerbapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/oauth"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/scheduling"
	_ "modernc.org/sqlite"
)

func scheduleFixture(t *testing.T) (*scheduling.Core, scheduling.Service) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "schedules.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	core, err := scheduling.New(context.Background(), db, nil, nil, scheduling.Options{})
	if err != nil {
		t.Fatal(err)
	}
	grants := scheduleSignedHost(t, []ScheduleGrant{{Subject: "client:fixture", App: "fixture", MaxJobs: 8}})
	return core, NewScheduleService(core, audit.NewMemory(), grants)
}
func fixtureJob() scheduling.Job {
	return scheduling.Job{ID: "job", Name: "Fixture", OwnerApp: "fixture", Timing: scheduling.Timing{Interval: time.Hour}, Target: scheduling.Target{Kind: scheduling.ResourceStart, ID: "fake"}, Timeout: time.Second, Enabled: true}
}
func scheduleSignedHost(t *testing.T, grants []ScheduleGrant) *ScheduleGrants {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := oauth.Config{Resource: "https://private.example/mcp", Builtin: true}
	issuer := &oauth.Issuer{Config: cfg, Key: key, Store: oauth.TokenStore{Dir: t.TempDir()}}
	keys := issuer.JWKS()
	verifier, err := oauth.NewVerifier(cfg, oauth.VerifierOptions{Builtin: &keys, Revoked: issuer.Store.Revoked})
	if err != nil {
		t.Fatal(err)
	}
	SetAuth(&Auth{Config: cfg, Issuer: issuer, Verifier: verifier})
	t.Cleanup(func() { SetAuth(nil) })
	for i := range grants {
		grants[i].Issuer = cfg.BuiltinIssuer()
	}
	bindings, err := NewScheduleGrants(grants)
	if err != nil {
		t.Fatal(err)
	}
	return bindings
}
func scheduleToken(t *testing.T, client string, scopes []string) (string, string) {
	t.Helper()
	token, record, err := ProcessAuth().Issuer.Issue(client, scopes, time.Hour, audit.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	return token, record.ID
}
func scheduleTokenContext(t *testing.T, token string) context.Context {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/schedules/v1/call", nil).WithContext(BeginRequest(context.Background(), SurfaceSocket))
	req.Header.Set(BearerHeader, token)
	verified, err := verifyBearer(req)
	if err != nil {
		t.Fatal(err)
	}
	return verified.Context()
}
func scheduleWebContext(t *testing.T) context.Context {
	token, _ := scheduleToken(t, "fixture", []string{oauth.ScopeOperate})
	return scheduleTokenContext(t, token)
}
func assertScheduleCode(ctx context.Context, t *testing.T, service scheduling.Service, req scheduling.Call, code string) {
	t.Helper()
	_, err := service.Schedule(ctx, req)
	if scheduling.ErrorCode(err) != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func TestScheduleCallerScopesLockdownAndPolicy(t *testing.T) {
	core, service := scheduleFixture(t)
	j := fixtureJob()
	create := scheduling.Call{Operation: "create", Job: &j, IdempotencyKey: "create", Acknowledged: true}
	ctx := scheduleWebContext(t)
	assertScheduleCode(context.Background(), t, service, create, "forbidden")
	unbound := WithPrincipal(context.Background(), Principal{Kind: PrincipalHuman, Via: ViaCLI, UIDVerified: true})
	assertScheduleCode(unbound, t, service, scheduling.Call{Operation: "list", OwnerApp: "fixture"}, "forbidden")
	withoutAck := create
	withoutAck.Acknowledged = false
	assertScheduleCode(ctx, t, service, withoutAck, "ack_required")
	readToken, _ := scheduleToken(t, "fixture", []string{oauth.ScopeRead})
	oauthCtx := scheduleTokenContext(t, readToken)
	assertScheduleCode(oauthCtx, t, service, create, string(ExternalConnectorInsufficientScope))
	if _, err := service.Schedule(oauthCtx, scheduling.Call{Operation: "list"}); err != nil {
		t.Fatal(err)
	}
	store := withBrakes(t)
	if _, _, err := EngageLockdown(ctx, audit.NewMemory(), store, "fixture incident"); err != nil {
		t.Fatal(err)
	}
	assertScheduleCode(ctx, t, service, create, string(ExternalConnectorLockdown))
	if _, err := service.Schedule(ctx, scheduling.Call{Operation: "list"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := core.Get(ctx, j.OwnerApp, j.ID); err != nil || found {
		t.Fatal("refused create persisted", err)
	}
	SetBrakes(nil)
	snapshotLane(t, policy.File{Version: policy.FileVersion, Enforcement: &policy.Enforcement{Enforce: []policy.EnforceEntry{{ID: "all", Principal: "agent"}}}, Providers: map[string]policy.Provider{"schedule": {Rules: []policy.Rule{{ID: "deny", Ops: []string{"create"}, Decision: policy.Deny}}}}})
	assertScheduleCode(ctx, t, service, create, string(ExternalConnectorPolicyDenied))
	SetEnforcement(nil)
	assertScheduleCode(ctx, t, service, scheduling.Call{Operation: "run_now", OwnerApp: j.OwnerApp, ID: j.ID, RequestID: "run", Acknowledged: true}, "forbidden")
}
func TestScheduleSocketBindingReadbackAndErrors(t *testing.T) {
	_, service := scheduleFixture(t)
	client := NewInProcessClient(WithScheduleService(service))
	path := startPeerSocket(t, client, nil)
	token, _ := scheduleToken(t, "fixture", []string{oauth.ScopeOperate})
	socket := NewSocketClient(path, WithBearer(func(context.Context) string { return token }), WithPrincipalClaim(func(context.Context) Principal { return Principal{Kind: PrincipalAgent, Via: ViaMCPStdio} }))
	j := fixtureJob()
	req := scheduling.Call{Operation: "create", Job: &j, IdempotencyKey: "fixture", Acknowledged: true}
	created, err := socket.Schedule(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := socket.Schedule(context.Background(), scheduling.Call{Operation: "get", OwnerApp: j.OwnerApp, ID: j.ID})
	if err != nil || got.Job.Revision != created.Job.Revision {
		t.Fatalf("socket readback %+v %v", got, err)
	}
	assertScheduleCode(context.Background(), t, socket, scheduling.Call{Operation: "run_now", OwnerApp: j.OwnerApp, ID: j.ID, RequestID: "run", Acknowledged: true}, "forbidden")
	assertScheduleCode(context.Background(), t, socket, scheduling.Call{Operation: "pause", OwnerApp: j.OwnerApp, ID: j.ID, Revision: "stale", Acknowledged: true}, "conflict")
	refusedPath := startPeerSocket(t, client, func(net.Conn) peerCred { return peerCred{uid: os.Getuid() + 1} })
	assertScheduleCode(context.Background(), t, NewSocketClient(refusedPath), scheduling.Call{Operation: "list"}, string(ExternalConnectorPrincipalRefused))
}

func TestScheduleBearerRevocationCheckedEveryCall(t *testing.T) {
	_, service := scheduleFixture(t)
	path := startPeerSocket(t, NewInProcessClient(WithScheduleService(service)), nil)
	token, id := scheduleToken(t, "fixture", []string{oauth.ScopeOperate})
	client := NewSocketClient(path, WithBearer(func(context.Context) string { return token }))
	if _, err := client.Schedule(context.Background(), scheduling.Call{Operation: "list"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessAuth().Issuer.Store.Revoke(id); err != nil {
		t.Fatal(err)
	}
	assertScheduleCode(context.Background(), t, client, scheduling.Call{Operation: "list"}, "forbidden")
}
func TestScheduleHTTPUnknownFieldsFailClosed(t *testing.T) {
	_, service := scheduleFixture(t)
	handler := ScheduleHTTP(service, "/schedules/v1/")
	for _, body := range []string{`{"operation":"list","authority":"operator"}`, `{"operation":"run_now"}`, `{"operation":"list"} {}`, `{"job":{"id":"j","permit":true}}`} {
		req := httptest.NewRequest(http.MethodPost, "/schedules/v1/list", strings.NewReader(body)).WithContext(scheduleWebContext(t))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s status %d", body, rec.Code)
		}
		var envelope scheduleEnvelope
		if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error == nil || envelope.Error.Code != "invalid" {
			t.Fatal(envelope)
		}
	}
}

type scheduleBodyProbe struct{ calls int }

func (p *scheduleBodyProbe) Schedule(context.Context, scheduling.Call) (scheduling.Result, error) {
	p.calls++
	return scheduling.Result{}, nil
}
func TestScheduleHTTPOverLimitNeverCallsService(t *testing.T) {
	for _, suffix := range []string{"", "{}"} {
		probe := &scheduleBodyProbe{}
		body := `{"operation":"list"}` + strings.Repeat(" ", 1<<20) + suffix
		req := httptest.NewRequest(http.MethodPost, "/schedules/v1/list", strings.NewReader(body))
		rec := httptest.NewRecorder()
		ScheduleHTTP(probe, "/schedules/v1/").ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || probe.calls != 0 {
			t.Fatalf("oversized body status=%d service calls=%d", rec.Code, probe.calls)
		}
	}
}
