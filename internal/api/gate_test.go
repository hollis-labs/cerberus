package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/service"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/supervise"
	"github.com/hollis-labs/cerberus/internal/target"
)

// The callers, as the auth guard puts them on a request.
var (
	ownerSession = libauth.Principal{Verified: true, ID: "user:u1", Kind: "user", SessionID: "s1", Scopes: []string{"*"}}
	apiKey       = libauth.Principal{Verified: true, ID: "key:k1", Kind: libauth.KindService, Scopes: []string{ScopeRead, ScopeOperate, ScopeAdmin}}
	otherKey     = libauth.Principal{Verified: true, ID: "key:k2", Kind: libauth.KindService, Scopes: []string{ScopeRead, ScopeOperate, ScopeAdmin}}
	localTrust   = libauth.Principal{Verified: true, ID: LocalTrustID, Kind: libauth.KindOperator, Scopes: []string{ScopeRead, ScopeOperate, ScopeAdmin}}
	keyToken     = libauth.Principal{Verified: true, ID: "key:k1", Kind: libauth.KindService, TokenID: "t1", Parent: "key:k1", Scopes: []string{ScopeRead}}
)

// The principal mapping: only the owner's console session is a human; every key, token and
// local-trust caller is an agent, named by its principal id.
func TestGatePrincipalMapping(t *testing.T) {
	for _, c := range []struct {
		name    string
		p       libauth.Principal
		ok      bool
		surface gate.CallerSurface
		kind    gate.PrincipalKind
		via     string
	}{
		{"owner session", ownerSession, true, gate.SurfaceWeb, gate.PrincipalHuman, gate.ViaWeb},
		{"api key", apiKey, true, gate.SurfaceSocket, gate.PrincipalAgent, ViaAPI},
		{"token minted from a key", keyToken, true, gate.SurfaceSocket, gate.PrincipalAgent, ViaAPI},
		{"token minted from the session", libauth.Principal{Verified: true, ID: "user:u1", Kind: "user", SessionID: "s1", TokenID: "tok"}, true, gate.SurfaceSocket, gate.PrincipalAgent, ViaAPI},
		{"local trust", localTrust, true, gate.SurfaceSocket, gate.PrincipalAgent, gate.ViaMCPStdio},
		{"no principal", libauth.Principal{}, false, gate.SurfaceSocket, gate.PrincipalAgent, gate.ViaUnknown},
	} {
		surface, p := GatePrincipal(c.p, c.ok)
		if surface != c.surface || p.Kind != c.kind || p.Via != c.via {
			t.Errorf("%s: %s %+v, want %s %s via %s", c.name, surface, p, c.surface, c.kind, c.via)
		}
	}
	if _, p := GatePrincipal(ownerSession, true); p.Session != "s1" || p.SelfReported {
		t.Errorf("the owner's session principal: %+v", p)
	}
	if _, p := GatePrincipal(apiKey, true); p.Client != "key:k1" || p.SelfReported {
		t.Errorf("an api key's principal: %+v", p)
	}
}

// constantPDP decides every request the same way (the old gate tests' constantPDP).
type constantPDP struct{ decision policy.Decision }

func (constantPDP) GlobalPosture() string { return policy.PostureSecure }

func (c constantPDP) Authorize(policy.Request) policy.Result {
	return policy.Result{Decision: c.decision, WouldBlock: c.decision != policy.Allow, Snapshot: "test",
		Matched: []policy.Match{{Rule: "test.rule", Decision: c.decision, Reason: "because the test says so"}}}
}

// enforceAll enforces policy on everything.
type enforceAll struct{}

func (enforceAll) Enforced(policy.Request) bool { return true }

// gatedAPI is the API over the fixture with a gate, and as a caller p.
type gatedAPI struct {
	h http.Handler
	w *gatetest.World
}

func newGatedAPI(t *testing.T, o gatetest.Options, resources []config.ResourceDef, mutator Mutator) *gatedAPI {
	t.Helper()
	w := gatetest.New(t, o)
	if resources == nil {
		resources = fixture()
	}
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return resources, nil }, Observer: fixedObserver{},
		Home: t.TempDir(), Gate: w.Gate, Mutator: mutator})
	if err != nil {
		t.Fatal(err)
	}
	return &gatedAPI{h: a.Mux(), w: w}
}

// call serves method path as p and decodes the flat error body's approval id, if any.
func (g *gatedAPI) call(t *testing.T, p libauth.Principal, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(libauth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	g.h.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Body)
	return rec.Code, string(out)
}

// pendingID calls as p and returns the approval the 409 approval_pending names under details.
func (g *gatedAPI) pendingID(t *testing.T, p libauth.Principal, method, path, reqBody string) string {
	t.Helper()
	code, body := g.call(t, p, method, path, reqBody)
	var e struct {
		Code    string `json:"code"`
		Details struct {
			Approval gate.ApprovalRef `json:"approval"`
		} `json:"details"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil || code != http.StatusConflict || e.Code != "approval_pending" || e.Details.Approval.ID == "" {
		t.Fatalf("want 409 approval_pending naming the approval, got %d %s", code, body)
	}
	return e.Details.Approval.ID
}

// L6 is read_sensitive (the old ResourceLogs, CERB-GAP-889): a read is recorded as local.logs
// with an intent and an outcome, and the redaction is kept.
func TestLogsAreRecordedAsReadSensitive(t *testing.T) {
	g := newGatedAPI(t, gatetest.Options{}, nil, nil)
	code, body := g.call(t, apiKey, "GET", "/api/resources/web/logs?lines=5&stream=stdout", "")
	if code != 200 || !strings.Contains(body, `"resource_id":"web"`) {
		t.Fatalf("logs = %d %s", code, body)
	}
	intents, outcomes := g.w.Records(audit.KindIntent), g.w.Records(audit.KindOutcome)
	if len(intents) != 1 || len(outcomes) != 1 {
		t.Fatalf("records: %+v", g.w.Sink.Records())
	}
	in := intents[0]
	if in.Connector != "local" || in.Operation != "logs" || in.Effect != "read_sensitive" || outcomes[0].OperationID != in.OperationID ||
		in.Principal.Kind != "agent" || in.Principal.Client != "key:k1" || in.Target.Resource != "web" {
		t.Fatalf("intent %+v, outcome %+v", in, outcomes[0])
	}
	// An unknown resource is still the old 404, and its read is recorded as a failed one.
	if code, body = g.call(t, apiKey, "GET", "/api/resources/nope/logs", ""); code != 404 || !strings.Contains(body, `"code":"not_found"`) {
		t.Fatalf("an unknown resource: %d %s", code, body)
	}
}

// The redaction of slice 4a holds under the gate: a secret-looking line is masked.
func TestLogsUnderTheGateAreStillRedacted(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "out.log")
	if err := os.WriteFile(logFile, []byte("starting\nAPI_KEY=sentinel-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := []config.ResourceDef{{ID: "dev", Type: "process", Connector: "local", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf},
		Config: map[string]any{"command": []any{"/bin/true"}, "log_file": logFile}}}
	g := newGatedAPI(t, gatetest.Options{}, res, nil)
	code, body := g.call(t, ownerSession, "GET", "/api/resources/dev/logs", "")
	if code != 200 || !strings.Contains(body, "starting") || strings.Contains(body, "sentinel-secret") {
		t.Fatalf("logs = %d %s", code, body)
	}
}

// Where policy is enforced, an agent reading a log needs an approval; the owner's console
// session approves it; the read under it with ?approval_id= is the requester's, spent once (the
// old TestResourceLogsNeedAnApprovalWhereEnforced and TestResourceLogsApprovalOverTheSocket).
func TestLogsNeedAnApprovalWhereEnforced(t *testing.T) {
	g := newGatedAPI(t, gatetest.Options{Policy: constantPDP{decision: policy.Approve}, Enforcement: enforceAll{}}, nil, nil)
	id := g.pendingID(t, apiKey, "GET", "/api/resources/web/logs?lines=5&stream=stdout", "")
	if code, body := g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true,"reason":"ok"}`); code != 200 || !strings.Contains(body, `"status":"approved"`) {
		t.Fatalf("the owner's approve: %d %s", code, body)
	}
	if code, body := g.call(t, otherKey, "GET", "/api/resources/web/logs?lines=5&stream=stdout&approval_id="+id, ""); code != 428 || !strings.Contains(body, `"code":"approval_required"`) {
		t.Fatalf("another caller read under the approval: %d %s", code, body)
	}
	if code, body := g.call(t, apiKey, "GET", "/api/resources/web/logs?lines=5&stream=stdout&approval_id="+id, ""); code != 200 || !strings.Contains(body, `"resource_id":"web"`) {
		t.Fatalf("the approved read: %d %s", code, body)
	}
	if got, _ := g.w.Broker.Get(id); got.Status != approval.Consumed {
		t.Fatalf("status %s", got.Status)
	}
	if code, body := g.call(t, apiKey, "GET", "/api/resources/web/logs?lines=5&stream=stdout&approval_id="+id, ""); code != 428 || !strings.Contains(body, `"code":"approval_required"`) {
		t.Fatalf("second read under one approval: %d %s", code, body)
	}
	// The pending refusal was recorded as refused, and the approved read as allowed under it.
	var ranUnder bool
	for _, r := range g.w.Records(audit.KindOutcome) {
		ranUnder = ranUnder || (r.ApprovalID == id && r.Decision == audit.DecisionAllowed)
	}
	if !ranUnder {
		t.Fatal("no outcome records the read under the approval")
	}
}

// laneOver is a supervise-mode lane over resources, with the gate g's.
func laneOver(t *testing.T, w *gatetest.World, resources []config.ResourceDef) *supervise.Service {
	t.Helper()
	base := t.TempDir()
	ctx := context.Background()
	cfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: filepath.Join(base, "cerberus.db"), CreateParentDir: true}}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fsys, root := storage.Migrations()
	if err = migrate.Ensure(ctx, cfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
		t.Fatal(err)
	}
	key, err := audit.OpenDigester(filepath.Join(base, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	service.SetStateRoot(base)
	t.Cleanup(func() { service.SetStateRoot("") })
	lane, err := supervise.New(supervise.Options{Mode: settings.ModeSupervise, Home: base, StateDir: base, Store: st, Digester: key, Gate: w.Gate,
		Config: func() (*config.ConfigV2, error) { return &config.ConfigV2{Resources: resources}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return lane
}

// The approval flow of a mutation, end to end through the API and the real lane under the
// built-in baseline: an agent's stop of a production resource is pending; a local-trust caller
// holding admin cannot complete it either (plan section 4.3); the owner's console session
// approves it; another caller cannot run under it; the requester runs it with approval_id, and
// the outcome records the approval it ran under.
func TestOwnerSessionApprovesAndTheApprovedMutationRuns(t *testing.T) {
	prod := config.ResourceDef{ID: "web", Type: "process", Project: "demo", Connector: "local",
		Env: target.EnvProd, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf},
		Config: map[string]any{"mode": "dev_session", "command": []any{"/bin/sleep", "300"}}}
	resources := []config.ResourceDef{prod}
	w := gatetest.New(t, gatetest.Options{})
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return resources, nil }, Observer: fixedObserver{},
		Home: t.TempDir(), Gate: w.Gate, Mutator: laneOver(t, w, resources)})
	if err != nil {
		t.Fatal(err)
	}
	g := &gatedAPI{h: a.Mux(), w: w}

	id := g.pendingID(t, apiKey, "POST", "/api/resources/web/stop", `{"acknowledged":true}`)
	g.pendingID(t, localTrust, "POST", "/api/resources/web/remove", `{"acknowledged":true}`)
	// The approval is listed and readable by anyone who may read, with the old shapes.
	if code, body := g.call(t, apiKey, "GET", "/api/approvals", ""); code != 200 || !strings.Contains(body, `"approvals":[`) || !strings.Contains(body, id) {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, body := g.call(t, apiKey, "GET", "/api/approvals/"+id, ""); code != 200 || !strings.Contains(body, `"status":"pending"`) || !strings.Contains(body, `"channel":"out_of_band"`) {
		t.Fatalf("get: %d %s", code, body)
	}
	if code, body := g.call(t, apiKey, "GET", "/api/approvals/nope", ""); code != 404 || !strings.Contains(body, `"code":"not_found"`) {
		t.Fatalf("get unknown: %d %s", code, body)
	}
	// Neither the requester nor local trust approves: they never reach the broker.
	for _, p := range []libauth.Principal{apiKey, localTrust, keyToken} {
		if code, body := g.call(t, p, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != 403 || !strings.Contains(body, `"code":"user_required"`) {
			t.Fatalf("%s approving: %d %s", p.ID, code, body)
		}
	}
	code, body := g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true,"reason":"go"}`)
	if code != 200 || !strings.Contains(body, `"status":"approved"`) {
		t.Fatalf("the owner's approve: %d %s", code, body)
	}
	if got, _ := w.Broker.Get(id); got.Decision == nil || !strings.HasPrefix(got.Decision.KeyFingerprint, "owner-session:") || got.Decision.By.Via != "web" {
		t.Fatalf("the decision does not carry the owner-session proof: %+v", got.Decision)
	}
	// Deciding again is the old 409.
	if code, body = g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != 409 || !strings.Contains(body, `"code":"approval_not_pending"`) {
		t.Fatalf("deciding twice: %d %s", code, body)
	}
	if code, body = g.call(t, otherKey, "POST", "/api/resources/web/stop", `{"acknowledged":true,"approval_id":"`+id+`"}`); code != 428 || !strings.Contains(body, `"code":"approval_required"`) {
		t.Fatalf("another caller ran under the approval: %d %s", code, body)
	}
	if code, body = g.call(t, apiKey, "POST", "/api/resources/web/stop", `{"acknowledged":true,"approval_id":"`+id+`"}`); code != 200 || !strings.Contains(body, `"success":true`) {
		t.Fatalf("the approved stop: %d %s", code, body)
	}
	if got, _ := w.Broker.Get(id); got.Status != approval.Consumed {
		t.Fatalf("status %s", got.Status)
	}
	var ranUnder bool
	for _, r := range w.Records(audit.KindOutcome) {
		ranUnder = ranUnder || (r.Operation == "stop" && r.ApprovalID == id && r.OutcomeCode == audit.OutcomeOK)
	}
	if !ranUnder {
		t.Fatalf("no outcome records the stop under the approval: %+v", w.Records(audit.KindOutcome))
	}
	// A plan request runs nothing and answers with the plan an approval binds to.
	if code, body = g.call(t, apiKey, "POST", "/api/resources/web/apply", `{"acknowledged":true,"plan":true}`); code != 200 || !strings.Contains(body, `"plan":{"plan_hash":"sha256:`) {
		t.Fatalf("plan: %d %s", code, body)
	}
	// Revoking is the owner's too; a revoke of a consumed approval is the old 409.
	if code, body = g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/revoke", `{}`); code != 409 || !strings.Contains(body, `"code":"approval_not_approved"`) {
		t.Fatalf("revoke consumed: %d %s", code, body)
	}
}
