package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/policy"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// secretsAPI is the API with an admin lane over the Cerberus keychain provider (the OS store,
// mocked for the process by TestMain), so the test sees the service and account it writes.
func secretsAPI(t *testing.T, o gatetest.Options) *gatedAPI {
	t.Helper()
	w := gatetest.New(t, o)
	root := filepath.Join(t.TempDir(), "adopted")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	svc, err := admin.New(admin.Options{Gate: w.Gate, Paths: admin.Paths{AdoptRoot: root, ConfigDir: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir(), Gate: w.Gate, Admin: svc})
	if err != nil {
		t.Fatal(err)
	}
	return &gatedAPI{h: a.Mux(), w: w}
}

// `cerberus secrets set`'s route is the owner's (the lead's ruling (a)). Under the baseline
// policy a key's write is asked for the owner's approval (409 approval_pending, the approval under
// details) and stores nothing while it is pending or decided by anyone but the owner; an MCP
// client is refused 428 naming both owner paths and asks for nothing; a write without the
// acknowledgment is refused before any approval is asked for. Once the owner approves on the
// console, the key's retry naming it stores the value under service "cerberus", account
// "<service>/<key>", for a name no installed connector declares (the bootstrap order: a
// backend's credential before its plugin); another value, or another key, under the approval is
// refused. The record is secrets.set, target the secret's name, and never holds the value.
func TestAKeySetsASecretOnlyWithTheOwnersApproval(t *testing.T) {
	g := secretsAPI(t, gatetest.Options{})
	const value = "ksm-config-value-0123456789"
	body := `{"value":"` + value + `","acknowledged":true}`
	path := "/api/secrets/keeper/ksm_config"
	stored := func() bool {
		_, err := keyring.Get("cerberus", "keeper/ksm_config")
		return err == nil
	}

	code, out := g.call(t, apiKey, "GET", path, "")
	if code != http.StatusOK || !strings.Contains(out, `"present":false`) {
		t.Fatalf("presence before: %d %s", code, out)
	}
	code, out = g.call(t, apiKey, "POST", path+"/plan", body)
	if code != http.StatusOK || !strings.Contains(out, `"plan_hash"`) || strings.Contains(out, value) {
		t.Fatalf("plan form: %d %s", code, out)
	}

	code, out = g.call(t, localTrust, "POST", path, body)
	wantCode(t, "an MCP client setting a secret", code, out, http.StatusPreconditionRequired, "approval_required")
	if !strings.Contains(out, "on the console") || !strings.Contains(out, "--approval") {
		t.Fatalf("the refusal does not name both owner paths: %s", out)
	}
	code, out = g.call(t, apiKey, "POST", path, `{"value":"`+value+`"}`)
	wantCode(t, "a write without the acknowledgment", code, out, http.StatusConflict, "acknowledgment_required")
	if len(g.w.Broker.List()) != 0 {
		t.Fatal("an MCP client's or an unacknowledged write asked the owner for an approval")
	}

	id := g.pendingID(t, apiKey, "POST", path, body)
	if a, ok := g.w.Broker.Get(id); !ok || a.Channel != "out_of_band" || a.Connector != "secrets" || a.Operation != "set" || a.PlanHash == "" {
		t.Fatalf("the secret's approval: %+v", a)
	}
	withApproval := `{"value":"` + value + `","acknowledged":true,"approval_id":"` + id + `"}`
	code, out = g.call(t, apiKey, "POST", path, withApproval)
	wantCode(t, "a key setting on a pending approval", code, out, http.StatusConflict, "approval_pending")
	code, out = g.call(t, apiKey, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`)
	wantCode(t, "a key approving its own write", code, out, http.StatusForbidden, "user_required")
	code, out = g.call(t, apiKey, "POST", path, withApproval)
	wantCode(t, "a key setting on its own decision", code, out, http.StatusConflict, "approval_pending")
	if stored() {
		t.Fatal("stored without the owner's approval")
	}

	if code, out = g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != http.StatusOK {
		t.Fatalf("the owner's approval: %d %s", code, out)
	}
	// The approval is for this value (the plan binds its keyed digest): another one is refused.
	code, out = g.call(t, apiKey, "POST", path, `{"value":"another","acknowledged":true,"approval_id":"`+id+`"}`)
	wantCode(t, "another value under the approval", code, out, http.StatusConflict, "plan_stale")
	code, out = g.call(t, localTrust, "POST", path, withApproval)
	wantCode(t, "an MCP client naming the owner's approval", code, out, http.StatusPreconditionRequired, "approval_required")
	code, out = g.call(t, otherKey, "POST", path, withApproval)
	wantCode(t, "another key naming the approval", code, out, http.StatusPreconditionRequired, "approval_required")
	if stored() {
		t.Fatal("a refused write stored the value")
	}

	code, out = g.call(t, apiKey, "POST", path, withApproval)
	var res admin.SecretSetResult
	if code != http.StatusOK || json.Unmarshal([]byte(out), &res) != nil || !res.Success || res.Name != "keeper/ksm_config" ||
		res.Length != len(value) || res.Existed || res.Proof != "approval:"+id {
		t.Fatalf("the approved write: %d %s", code, out)
	}
	if got, err := keyring.Get("cerberus", "keeper/ksm_config"); err != nil || got != value {
		t.Fatalf("the keyring entry (service cerberus, account keeper/ksm_config): %q %v", got, err)
	}
	if a, _ := g.w.Broker.Get(id); a.Status != "consumed" {
		t.Fatalf("the approval was not spent: %s", a.Status)
	}
	code, out = g.call(t, apiKey, "POST", path, withApproval)
	wantCode(t, "a second write under the spent approval", code, out, http.StatusPreconditionRequired, "approval_required")
	if code, out = g.call(t, apiKey, "GET", path, ""); code != http.StatusOK || !strings.Contains(out, `"present":true`) || strings.Contains(out, value) {
		t.Fatalf("presence after: %d %s", code, out)
	}

	var ok bool
	for _, kind := range []string{audit.KindIntent, audit.KindOutcome} {
		for _, r := range g.w.Records(kind) {
			raw, _ := json.Marshal(r)
			if strings.Contains(string(raw), value) {
				t.Fatalf("a record holds the value: %s", raw)
			}
			if r.Connector != "secrets" || r.Operation != "set" {
				continue
			}
			if r.Effect != "admin" || r.Target.Kind != "secret" || r.Target.Fields["name"] != "keeper/ksm_config" {
				t.Fatalf("the secrets.set record: %s", raw)
			}
			ok = ok || (kind == audit.KindOutcome && r.OutcomeCode == "ok")
		}
	}
	if !ok {
		t.Fatalf("no secrets.set outcome ok: %+v", g.w.Sink.Records())
	}
}

// The owner signed in on the console sets a secret directly, with the acknowledgment, and
// setting it again reports that a value was replaced.
func TestTheOwnerSetsASecretOnTheConsole(t *testing.T) {
	g := secretsAPI(t, gatetest.Options{})
	path := "/api/secrets/onepassword/service_account_token"
	code, out := g.call(t, ownerSession, "POST", path, `{"value":"tok-a"}`)
	wantCode(t, "the owner without the acknowledgment", code, out, http.StatusConflict, "acknowledgment_required")
	if _, err := keyring.Get("cerberus", "onepassword/service_account_token"); err == nil {
		t.Fatal("an unacknowledged write stored the value")
	}
	for i, want := range []string{`"existed":false`, `"existed":true`} {
		code, out = g.call(t, ownerSession, "POST", path, `{"value":"tok-`+string(rune('b'+i))+`","acknowledged":true}`)
		if code != http.StatusOK || !strings.Contains(out, want) || !strings.Contains(out, `"proof":"console"`) {
			t.Fatalf("set %d: %d %s", i, code, out)
		}
	}
	if got, _ := keyring.Get("cerberus", "onepassword/service_account_token"); got != "tok-c" {
		t.Fatalf("keyring (service cerberus, account onepassword/service_account_token) = %q", got)
	}
	if len(g.w.Broker.List()) != 0 {
		t.Fatal("the owner on the console was asked for an approval")
	}
}

// Malformed names and an empty value are refused before the gate, with old's messages.
func TestSecretSetRefusesMalformedNames(t *testing.T) {
	g := secretsAPI(t, gatetest.Options{})
	for _, path := range []string{
		"/api/secrets/Keeper/ksm_config", "/api/secrets/keeper/-x", "/api/secrets/keeper/" + strings.Repeat("a", 65),
		"/api/secrets/ke%20eper/x", "/api/secrets/_x/y",
	} {
		code, out := g.call(t, ownerSession, "POST", path, `{"value":"v"}`)
		wantCode(t, path, code, out, http.StatusBadRequest, "invalid_args")
		if !strings.Contains(out, "is not a secret name") {
			t.Fatalf("%s: %s", path, out)
		}
	}
	code, out := g.call(t, ownerSession, "POST", "/api/secrets/keeper/ksm_config", `{"value":""}`)
	wantCode(t, "empty value", code, out, http.StatusBadRequest, "invalid_args")
	if !strings.Contains(out, "the value is empty; nothing was stored") {
		t.Fatal(out)
	}
	if n := len(g.w.Sink.Records()); n != 0 {
		t.Fatalf("a refusal before the gate wrote %d records", n)
	}
}

// Observe mode has no admin lane: 503 admin_unavailable.
func TestSecretSetInObserveMode(t *testing.T) {
	a := newTestAPI(t, "")
	code, out := (&gatedAPI{h: a.Mux()}).call(t, apiKey, "POST", "/api/secrets/keeper/ksm_config", `{"value":"v"}`)
	wantCode(t, "observe mode", code, out, http.StatusServiceUnavailable, "admin_unavailable")
}

// approveSecretsSet is a policy whose rule enforces approve on secrets.set, its baseline letting
// an agent run admin operations, so that rule alone decides the write.
func approveSecretsSet(t *testing.T) policy.PDP {
	t.Helper()
	f := policy.File{Version: policy.FileVersion,
		Baseline: &policy.BaselineOverride{ByEffect: map[contract.Effect]map[string]policy.Decision{contract.EffectAdmin: {"agent": policy.Allow}}},
		Providers: map[string]policy.Provider{
			"secrets": {Rules: []policy.Rule{{ID: "approve-secrets-set", Ops: []string{"set"}, Decision: policy.Approve, Reason: "the test asks for it"}}}}}
	if problems := f.Validate(); len(problems) > 0 {
		t.Fatal(problems)
	}
	return policy.NewEvaluator(f, "test")
}

// The lead's ruling (a): where a policy rule enforces approve on an owner-only write, the owner's
// approval meets it too. A key's write asks for one approval, the owner's, out of band, naming
// the policy rule beside the owner's; once the owner approves it on the console, the retry naming
// it stores the value with that one approval, written on the outcome with its plan hash; a second
// use of the same approval is refused, and nothing asks for a second approval. The owner on the
// console is no shortcut past the policy: the console session alone is not an approval.
func TestTheOwnersApprovalMeetsAPolicyApproveOnSecretsSet(t *testing.T) {
	g := secretsAPI(t, gatetest.Options{Policy: approveSecretsSet(t), Enforcement: enforceAll{}})
	const value = "ksm-config-value-policy"
	body := `{"value":"` + value + `","acknowledged":true}`
	path := "/api/secrets/keeper/ksm_config"

	id := g.pendingID(t, apiKey, "POST", path, body)
	a, ok := g.w.Broker.Get(id)
	if !ok || a.Channel != "out_of_band" || a.Rule != "secrets.set" || a.PlanHash == "" || !strings.Contains(a.Reason, "approve-secrets-set") {
		t.Fatalf("the one approval: %+v", a)
	}
	if n := len(g.w.Broker.List()); n != 1 {
		t.Fatalf("asking opened %d approvals, want the owner's alone", n)
	}
	if code, out := g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != http.StatusOK {
		t.Fatalf("the owner's approval: %d %s", code, out)
	}

	withApproval := `{"value":"` + value + `","acknowledged":true,"approval_id":"` + id + `"}`
	code, out := g.call(t, apiKey, "POST", path, withApproval)
	var res admin.SecretSetResult
	if code != http.StatusOK || json.Unmarshal([]byte(out), &res) != nil || !res.Success || res.Proof != "approval:"+id {
		t.Fatalf("the approved write under the policy: %d %s", code, out)
	}
	if got, err := keyring.Get("cerberus", "keeper/ksm_config"); err != nil || got != value {
		t.Fatalf("the keyring entry: %q %v", got, err)
	}
	if a, _ = g.w.Broker.Get(id); a.Status != "consumed" {
		t.Fatalf("the approval was not spent: %s", a.Status)
	}
	var spent bool
	for _, r := range g.w.Records(audit.KindOutcome) {
		if r.Connector == "secrets" && r.Operation == "set" && r.OutcomeCode == "ok" {
			spent = r.ApprovalID == id && r.PlanHash == a.PlanHash
		}
	}
	if !spent {
		t.Fatalf("the outcome does not carry the approval and its plan: %+v", g.w.Records(audit.KindOutcome))
	}

	code, out = g.call(t, apiKey, "POST", path, withApproval)
	wantCode(t, "a second use of the same approval", code, out, http.StatusPreconditionRequired, "approval_required")
	if n := len(g.w.Broker.List()); n != 1 {
		t.Fatalf("%d approvals: the refused second use asked for another", n)
	}

	// The console session is not an approval: under the policy the owner, too, is asked for one.
	code, out = g.call(t, ownerSession, "POST", path, `{"value":"tok","acknowledged":true}`)
	wantCode(t, "the owner on the console under the policy", code, out, http.StatusConflict, "approval_pending")
	if got, _ := keyring.Get("cerberus", "keeper/ksm_config"); got != value {
		t.Fatalf("the console write stored without an approval: %q", got)
	}
}
