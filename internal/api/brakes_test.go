package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// devResource is a dev-session resource the brake tests try to stop; the gate refuses every
// stop before it runs, so nothing is started or stopped.
var devResource = config.ResourceDef{ID: "web", Type: "process", Project: "shop", Connector: "local",
	Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf},
	Config: map[string]any{"mode": "dev_session", "command": []any{"/bin/sleep", "300"}}}

// brakedAPI is the API over a real gate and a supervise-mode lane over devResource.
func brakedAPI(t *testing.T, o gatetest.Options) *gatedAPI {
	t.Helper()
	w := gatetest.New(t, o)
	resources := []config.ResourceDef{devResource}
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return resources, nil }, Observer: fixedObserver{},
		Home: t.TempDir(), Gate: w.Gate, Mutator: laneOver(t, w, resources)})
	if err != nil {
		t.Fatal(err)
	}
	return &gatedAPI{h: a.Mux(), w: w}
}

// brakeView decodes a 200 brake answer.
func brakeView(t *testing.T, call func() (int, string)) gate.BrakesView {
	t.Helper()
	code, body := call()
	var v gate.BrakesView
	if code != http.StatusOK || json.Unmarshal([]byte(body), &v) != nil {
		t.Fatalf("want 200 with the brakes, got %d %s", code, body)
	}
	return v
}

// brakeRecords are the brake_changed records with operation op.
func brakeRecords(w *gatetest.World, op string) []audit.Record {
	var out []audit.Record
	for _, r := range w.Records(audit.KindBrakeChanged) {
		if r.Operation == op {
			out = append(out, r)
		}
	}
	return out
}

func wantCode(t *testing.T, what string, code int, body string, status int, errCode string) {
	t.Helper()
	if code != status || !strings.Contains(body, `"code":"`+errCode+`"`) {
		t.Fatalf("%s: %d %s, want %d %s", what, code, body, status, errCode)
	}
}

// Behind the real guard each brake route needs its scope: a read-only key reads the brakes and
// is refused every change with 403; a key holding cerberus.admin engages.
func TestBrakeRoutesNeedTheirScopes(t *testing.T) {
	w := gatetest.New(t, gatetest.Options{})
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir(), Gate: w.Gate})
	if err != nil {
		t.Fatal(err)
	}
	table := libauth.NewScopeTable()
	a.RegisterScopes(table)
	guard, err := libauth.New(libauth.Config{Mode: libauth.ModeEnforce, Scopes: table, Verifier: libauth.NewAPIKeyVerifier(
		libauth.APIKey{Hash: libauth.HashKey("key_read"), ID: "key:read", Scopes: []string{ScopeRead}},
		libauth.APIKey{Hash: libauth.HashKey("key_operate"), ID: "key:operate", Scopes: []string{ScopeRead, ScopeOperate}},
		libauth.APIKey{Hash: libauth.HashKey("key_admin"), ID: "key:admin", Scopes: []string{ScopeRead, ScopeOperate, ScopeAdmin}},
	)})
	if err != nil {
		t.Fatal(err)
	}
	h := guard.Middleware(a.Mux())
	send := func(key, method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := send("key_read", "GET", "/api/brakes", ""); code != 200 || !strings.Contains(body, `"state":{`) {
		t.Fatalf("a read key reading the brakes: %d %s", code, body)
	}
	for _, path := range []string{"/api/brakes/lockdown", "/api/brakes/lockdown/lift", "/api/brakes/freeze", "/api/brakes/freeze/frz_1/lift", "/api/brakes/suspensions/sus_1/reset"} {
		for _, key := range []string{"key_read", "key_operate"} {
			if code, body := send(key, "POST", path, `{}`); code != http.StatusForbidden {
				t.Errorf("%s on POST %s: %d %s, want 403", key, path, code, body)
			}
		}
	}
	if st, _ := w.Brakes.Load(); st.Engaged() {
		t.Fatal("a refused key engaged a brake")
	}
	brakeView(t, func() (int, string) { return send("key_admin", "POST", "/api/brakes/lockdown", `{"reason":"drill"}`) })
}

// Engaging is open to anyone holding admin, an agent included, as old's EngageLockdown was: an
// API key engages, the lockdown is recorded as that key's brake_changed, engaging again is the
// same lockdown, and a mutation under it is 423. Lifting is the owner's (the lead's ruling on
// decision 0.1): the key's lift asks for an out-of-band approval (409 approval_pending naming
// it under details), a pending approval lifts nothing, the key cannot approve it itself (403
// user_required), and once the owner approves it on the console the key's retry naming it
// lifts, recorded with the approval as its proof. Local trust (MCP stdio) is refused outright
// (428, naming both owner paths) and asks for nothing.
func TestAnAPIKeyEngagesALockdownAndLiftsItOnlyWithTheOwnersApproval(t *testing.T) {
	g := brakedAPI(t, gatetest.Options{})
	v := brakeView(t, func() (int, string) {
		return g.call(t, apiKey, "POST", "/api/brakes/lockdown", `{"reason":"agent loop"}`)
	})
	if v.State.Lockdown == nil || v.State.Lockdown.Reason != "agent loop" || v.State.Lockdown.By.Kind != "agent" || v.State.Lockdown.By.Via != ViaAPI {
		t.Fatalf("lockdown %+v", v.State.Lockdown)
	}
	lid := v.State.Lockdown.ID
	recs := brakeRecords(g.w, "lockdown")
	if len(recs) != 1 || recs[0].Note != "LOCKDOWN engaged: agent loop" || recs[0].Principal.Client != "key:k1" || recs[0].Effect != string(contract.EffectAdmin) ||
		recs[0].Target.Kind != "brake.lockdown" || recs[0].Target.Fields["id"] != lid || len(recs[0].Brakes) == 0 {
		t.Fatalf("brake_changed: %+v", recs)
	}
	if again := brakeView(t, func() (int, string) { return g.call(t, otherKey, "POST", "/api/brakes/lockdown", `{}`) }); again.State.Lockdown.ID != lid {
		t.Fatalf("engaging again made another lockdown: %+v", again.State.Lockdown)
	}
	code, body := g.call(t, apiKey, "POST", "/api/resources/web/stop", `{"acknowledged":true}`)
	wantCode(t, "a stop under the lockdown", code, body, http.StatusLocked, "lockdown")
	if v = brakeView(t, func() (int, string) { return g.call(t, apiKey, "GET", "/api/brakes", "") }); v.State.Lockdown == nil || v.State.Lockdown.ID != lid {
		t.Fatalf("GET /api/brakes: %+v", v.State)
	}

	code, body = g.call(t, localTrust, "POST", "/api/brakes/lockdown/lift", `{}`)
	wantCode(t, "local trust lifting", code, body, http.StatusPreconditionRequired, "approval_required")
	if !strings.Contains(body, "on the console") || !strings.Contains(body, "--approval") {
		t.Fatalf("the refusal does not name both owner paths: %s", body)
	}
	if len(g.w.Broker.List()) != 0 {
		t.Fatal("an MCP client's lift asked the owner for an approval")
	}

	id := g.pendingID(t, apiKey, "POST", "/api/brakes/lockdown/lift", `{}`)
	if a, ok := g.w.Broker.Get(id); !ok || a.Channel != "out_of_band" || a.Operation != "lift_lockdown" {
		t.Fatalf("the lift approval: %+v", a)
	}
	code, body = g.call(t, apiKey, "POST", "/api/brakes/lockdown/lift", `{"approval_id":"`+id+`"}`)
	wantCode(t, "a key lifting on a pending approval", code, body, http.StatusConflict, "approval_pending")
	code, body = g.call(t, apiKey, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`)
	wantCode(t, "a key approving its own lift", code, body, http.StatusForbidden, "user_required")
	if st, _ := g.w.Brakes.Load(); st.Lockdown == nil {
		t.Fatal("the lockdown was lifted without the owner's approval")
	}
	if code, body = g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != 200 {
		t.Fatalf("the owner's approve: %d %s", code, body)
	}
	code, body = g.call(t, localTrust, "POST", "/api/brakes/lockdown/lift", `{"approval_id":"`+id+`"}`)
	wantCode(t, "local trust naming the owner's approval", code, body, http.StatusPreconditionRequired, "approval_required")
	code, body = g.call(t, otherKey, "POST", "/api/brakes/lockdown/lift", `{"approval_id":"`+id+`"}`)
	wantCode(t, "another key naming the approval", code, body, http.StatusPreconditionRequired, "approval_required")
	if v = brakeView(t, func() (int, string) {
		return g.call(t, apiKey, "POST", "/api/brakes/lockdown/lift", `{"approval_id":"`+id+`"}`)
	}); v.State.Lockdown != nil {
		t.Fatalf("still locked: %+v", v.State)
	}
	lifts := brakeRecords(g.w, "lift_lockdown")
	if len(lifts) != 1 || lifts[0].Note != "LOCKDOWN lifted (approval:"+id+")" || lifts[0].Principal.Client != "key:k1" || lifts[0].Principal.Via != ViaAPI {
		t.Fatalf("brake_changed: %+v", lifts)
	}
	code, body = g.call(t, apiKey, "POST", "/api/resources/web/stop", `{"acknowledged":true}`)
	if code == http.StatusLocked {
		t.Fatalf("a stop after the lift: %d %s", code, body)
	}
}

// A lift is the owner's: the console session asks (409 approval_pending naming the
// out-of-band approval under details), the owner approves it on the console, and the session's
// retry naming it lifts. An API key cannot run under that approval, and the lift is recorded
// with the approval as its proof. Lifting again is old's 409.
func TestTheOwnerLiftsALockdownWithTheConsoleApproval(t *testing.T) {
	g := brakedAPI(t, gatetest.Options{})
	brakeView(t, func() (int, string) { return g.call(t, apiKey, "POST", "/api/brakes/lockdown", `{}`) })
	id := g.pendingID(t, ownerSession, "POST", "/api/brakes/lockdown/lift", `{}`)
	if a, ok := g.w.Broker.Get(id); !ok || a.Channel != "out_of_band" || a.Operation != "lift_lockdown" {
		t.Fatalf("the lift approval: %+v", a)
	}
	code, body := g.call(t, ownerSession, "POST", "/api/brakes/lockdown/lift", `{"approval_id":"`+id+`"}`)
	wantCode(t, "a lift before the approval is decided", code, body, http.StatusConflict, "approval_pending")
	code, body = g.call(t, apiKey, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`)
	wantCode(t, "a key approving the lift", code, body, http.StatusForbidden, "user_required")
	if code, body = g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != 200 {
		t.Fatalf("the owner's approve: %d %s", code, body)
	}
	code, body = g.call(t, apiKey, "POST", "/api/brakes/lockdown/lift", `{"approval_id":"`+id+`"}`)
	wantCode(t, "a key lifting under the owner's approval", code, body, http.StatusPreconditionRequired, "approval_required")
	if v := brakeView(t, func() (int, string) {
		return g.call(t, ownerSession, "POST", "/api/brakes/lockdown/lift", `{"approval_id":"`+id+`"}`)
	}); v.State.Lockdown != nil {
		t.Fatalf("still locked: %+v", v.State)
	}
	recs := brakeRecords(g.w, "lift_lockdown")
	if len(recs) != 1 || recs[0].Note != "LOCKDOWN lifted (approval:"+id+")" || recs[0].Principal.Kind != "human" || recs[0].Principal.Via != "web" {
		t.Fatalf("brake_changed: %+v", recs)
	}
	code, body = g.call(t, ownerSession, "POST", "/api/brakes/lockdown/lift", `{"approval_id":"`+id+`"}`)
	wantCode(t, "lifting again", code, body, http.StatusConflict, "not_engaged")
	code, body = g.call(t, apiKey, "POST", "/api/resources/web/stop", `{"acknowledged":true}`)
	if code == http.StatusLocked {
		t.Fatalf("a stop after the lift: %d %s", code, body)
	}
}

// A freeze needs a match (old's 400), is engaged by a key on the targets it selects, refuses a
// mutation of them with 423 frozen, and is lifted as a lockdown is: the key's lift waits on the
// owner's console approval and goes through naming it. An unknown freeze is old's 409.
func TestAFreezeIsEngagedAndLiftedLikeALockdown(t *testing.T) {
	g := brakedAPI(t, gatetest.Options{})
	code, body := g.call(t, apiKey, "POST", "/api/brakes/freeze", `{"reason":"release"}`)
	wantCode(t, "a freeze without a match", code, body, http.StatusBadRequest, "invalid_args")
	if !strings.Contains(body, "a freeze needs a match") {
		t.Fatalf("message: %s", body)
	}
	code, body = g.call(t, apiKey, "POST", "/api/brakes/freeze", `{not json`)
	wantCode(t, "a body that does not parse", code, body, http.StatusBadRequest, "invalid_args")
	v := brakeView(t, func() (int, string) {
		return g.call(t, apiKey, "POST", "/api/brakes/freeze", `{"reason":"release","match":{"id":"web"}}`)
	})
	if len(v.State.Freezes) != 1 || v.State.Freezes[0].Match.ID != "web" {
		t.Fatalf("freezes %+v", v.State.Freezes)
	}
	fid := v.State.Freezes[0].ID
	if recs := brakeRecords(g.w, "freeze"); len(recs) != 1 || recs[0].Note != "FREEZE "+fid+" engaged on id=web: release" || recs[0].Target.Fields["id"] != fid {
		t.Fatalf("brake_changed: %+v", recs)
	}
	code, body = g.call(t, apiKey, "POST", "/api/resources/web/stop", `{"acknowledged":true}`)
	wantCode(t, "a stop of the frozen resource", code, body, http.StatusLocked, "frozen")

	code, body = g.call(t, localTrust, "POST", "/api/brakes/freeze/"+fid+"/lift", `{}`)
	wantCode(t, "local trust lifting the freeze", code, body, http.StatusPreconditionRequired, "approval_required")
	code, body = g.call(t, apiKey, "POST", "/api/brakes/freeze/frz_nope/lift", `{}`)
	wantCode(t, "an unknown freeze", code, body, http.StatusConflict, "not_engaged")
	id := g.pendingID(t, apiKey, "POST", "/api/brakes/freeze/"+fid+"/lift", `{}`)
	code, body = g.call(t, apiKey, "POST", "/api/brakes/freeze/"+fid+"/lift", `{"approval_id":"`+id+`"}`)
	wantCode(t, "a key lifting the freeze on a pending approval", code, body, http.StatusConflict, "approval_pending")
	code, body = g.call(t, apiKey, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`)
	wantCode(t, "a key approving its own lift", code, body, http.StatusForbidden, "user_required")
	if code, body = g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != 200 {
		t.Fatalf("the owner's approve: %d %s", code, body)
	}
	if v = brakeView(t, func() (int, string) {
		return g.call(t, apiKey, "POST", "/api/brakes/freeze/"+fid+"/lift", `{"approval_id":"`+id+`"}`)
	}); len(v.State.Freezes) != 0 {
		t.Fatalf("still frozen: %+v", v.State.Freezes)
	}
	if recs := brakeRecords(g.w, "lift_freeze"); len(recs) != 1 || recs[0].Note != "FREEZE "+fid+" lifted (approval:"+id+")" {
		t.Fatalf("brake_changed: %+v", recs)
	}
}

// breakerFile denies an agent's lifecycle calls and trips after three real denials in ten
// minutes.
func breakerFile(t *testing.T) policy.PDP {
	t.Helper()
	f := policy.File{Version: policy.FileVersion, CircuitBreaker: &policy.CircuitBreaker{Denials: 3, Window: 10 * time.Minute},
		Principals: []policy.PrincipalBlock{{Match: policy.PrincipalMatch{Kind: "agent"},
			Rules: []policy.Rule{{ID: "no-agent-lifecycle", Effect: []contract.Effect{contract.EffectLifecycle}, Decision: policy.Deny, Reason: "not for agents"}}}}}
	if problems := f.Validate(); len(problems) > 0 {
		t.Fatal(problems)
	}
	return policy.NewEvaluator(f, "test")
}

// The circuit breaker suspends an API key (an agent, named by its principal id) after three real
// denials: its next mutation is 423 session_suspended, another key is untouched, and the
// suspension is listed. Local trust (MCP stdio) is refused a reset (428); the owner's console
// session needs the typed phrase; a key's reset asks for the owner's approval, waits while it
// is pending, cannot approve it itself, and goes through once the owner approves on the
// console, and the key is no longer suspended. The trip and the reset are brake_changed
// records, as old wrote them.
func TestTheBreakerSuspendsAnAPIKeyAndTheOwnerApprovesItsReset(t *testing.T) {
	g := brakedAPI(t, gatetest.Options{Policy: breakerFile(t), Enforcement: enforceAll{}})
	stop := func(p libauth.Principal) (int, string) {
		return g.call(t, p, "POST", "/api/resources/web/stop", `{"acknowledged":true}`)
	}
	for i := 0; i < 3; i++ {
		code, body := stop(apiKey)
		wantCode(t, "a denied stop", code, body, http.StatusForbidden, "policy_denied")
	}
	code, body := stop(apiKey)
	wantCode(t, "the suspended key's stop", code, body, http.StatusLocked, "session_suspended")
	code, body = stop(otherKey)
	wantCode(t, "another key's stop", code, body, http.StatusForbidden, "policy_denied")

	v := brakeView(t, func() (int, string) { return g.call(t, apiKey, "GET", "/api/brakes", "") })
	if len(v.State.Suspensions) != 1 || v.State.Suspensions[0].Key != "agent|api|session:key:k1" || v.State.Suspensions[0].Denials != 3 {
		t.Fatalf("suspensions %+v", v.State.Suspensions)
	}
	sid := v.State.Suspensions[0].ID
	if _, refusal := stop(apiKey); !strings.Contains(refusal, "cerberus breaker reset "+sid) {
		t.Fatalf("the refusal does not name the reset: %s", refusal)
	}
	trips := brakeRecords(g.w, "suspend")
	if len(trips) != 1 || !strings.HasPrefix(trips[0].Note, "SESSION SUSPENDED: agent over api (agent|api|session:key:k1) after 3 policy denials") ||
		trips[0].Principal.Kind != "automation" || trips[0].Target.Fields["id"] != sid {
		t.Fatalf("the trip: %+v", trips)
	}

	reset := func(p libauth.Principal, body string) (int, string) {
		return g.call(t, p, "POST", "/api/brakes/suspensions/"+sid+"/reset", body)
	}
	code, body = reset(localTrust, `{"typed":"reset `+sid+`"}`)
	wantCode(t, "local trust resetting", code, body, http.StatusPreconditionRequired, "approval_required")
	if !strings.Contains(body, "on the console") || !strings.Contains(body, "--approval") {
		t.Fatalf("the refusal does not name both owner paths: %s", body)
	}
	code, body = g.call(t, ownerSession, "POST", "/api/brakes/suspensions/"+sid+"/reset", `{"typed":"yes"}`)
	wantCode(t, "the owner with the wrong phrase", code, body, http.StatusPreconditionRequired, "approval_required")
	if !strings.Contains(body, "reset "+sid) {
		t.Fatalf("the refusal does not name the phrase: %s", body)
	}
	code, body = g.call(t, ownerSession, "POST", "/api/brakes/suspensions/sus_nope/reset", `{"typed":"reset sus_nope"}`)
	wantCode(t, "an unknown suspension", code, body, http.StatusConflict, "not_engaged")

	// A key (the phrase is no proof from a key) asks for the owner's approval; it resets
	// nothing until the owner approves on the console.
	id := g.pendingID(t, otherKey, "POST", "/api/brakes/suspensions/"+sid+"/reset", `{"typed":"reset `+sid+`"}`)
	if a, ok := g.w.Broker.Get(id); !ok || a.Channel != "out_of_band" || a.Operation != "reset_suspension" {
		t.Fatalf("the reset approval: %+v", a)
	}
	code, body = reset(otherKey, `{"approval_id":"`+id+`"}`)
	wantCode(t, "a key resetting on a pending approval", code, body, http.StatusConflict, "approval_pending")
	code, body = g.call(t, otherKey, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`)
	wantCode(t, "a key approving its own reset", code, body, http.StatusForbidden, "user_required")
	if st, _ := g.w.Brakes.Load(); len(st.Suspensions) != 1 {
		t.Fatal("a refused reset reset the suspension")
	}
	if code, body = g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != 200 {
		t.Fatalf("the owner's approve: %d %s", code, body)
	}
	if v = brakeView(t, func() (int, string) { return reset(otherKey, `{"approval_id":"`+id+`"}`) }); len(v.State.Suspensions) != 0 {
		t.Fatalf("still suspended: %+v", v.State.Suspensions)
	}
	resets := brakeRecords(g.w, "reset_suspension")
	if len(resets) != 1 || resets[0].Note != "SESSION RESET: agent|api|session:key:k1 ("+sid+") (approval:"+id+")" || resets[0].Principal.Client != "key:k2" ||
		resets[0].Target.Fields["caller"] != "agent|api|session:key:k1" {
		t.Fatalf("the reset: %+v", resets)
	}
	// The reset's brake_changed note names the approval it ran under, as a lift's does; the
	// chain's approval_consumed record (and the brake store's proof) carry it too.
	var consumed bool
	for _, r := range g.w.Records(audit.KindApprovalConsumed) {
		consumed = consumed || (r.Approval != nil && r.Approval.ID == id)
	}
	if !consumed {
		t.Fatalf("no approval_consumed for %s: %+v", id, g.w.Records(audit.KindApprovalConsumed))
	}
	code, body = stop(apiKey)
	wantCode(t, "the key's stop after the reset", code, body, http.StatusForbidden, "policy_denied")
}

// Observe mode holds no brakes: every brake route answers 503 brakes_unavailable, as the
// approvals routes answer approvals_unavailable, and writes nothing.
func TestBrakeRoutesInObserveMode(t *testing.T) {
	a := newTestAPI(t, "")
	for _, c := range [][2]string{
		{"GET", "/api/brakes"}, {"POST", "/api/brakes/lockdown"}, {"POST", "/api/brakes/lockdown/lift"},
		{"POST", "/api/brakes/freeze"}, {"POST", "/api/brakes/freeze/frz_1/lift"}, {"POST", "/api/brakes/suspensions/sus_1/reset"},
	} {
		req := httptest.NewRequest(c[0], c[1], strings.NewReader(`{"match":{"id":"web"}}`))
		req = req.WithContext(libauth.WithPrincipal(req.Context(), ownerSession))
		rec := httptest.NewRecorder()
		a.Mux().ServeHTTP(rec, req)
		wantCode(t, c[0]+" "+c[1], rec.Code, rec.Body.String(), http.StatusServiceUnavailable, "brakes_unavailable")
	}
}
