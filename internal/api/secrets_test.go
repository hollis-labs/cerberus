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

// `cerberus secrets set`'s route: a gated admin write. A key is asked for the owner's approval
// (409 approval_pending) and stores nothing until the owner approves it on the console; its retry
// with the approval stores the value under service "cerberus", account "<service>/<key>", for a
// name no installed connector declares (the bootstrap order: a backend's credential before its
// plugin). The record is secrets.set, target the secret's name, and never holds the value.
func TestSecretSetIsAGatedAdminWrite(t *testing.T) {
	// Policy asks for an approval of every call, as an operator's policy may for an admin write.
	g := secretsAPI(t, gatetest.Options{Policy: constantPDP{decision: policy.Approve}, Enforcement: enforceAll{}})
	const value = "ksm-config-value-0123456789"
	body := `{"value":"` + value + `"}`
	path := "/api/secrets/keeper/ksm_config"

	code, out := g.call(t, apiKey, "GET", path, "")
	if code != http.StatusOK || !strings.Contains(out, `"present":false`) {
		t.Fatalf("presence before: %d %s", code, out)
	}
	code, out = g.call(t, apiKey, "POST", path+"/plan", body)
	if code != http.StatusOK || !strings.Contains(out, `"plan_hash"`) || strings.Contains(out, value) {
		t.Fatalf("plan form: %d %s", code, out)
	}
	id := g.pendingID(t, apiKey, "POST", path, body)
	if _, err := keyring.Get("cerberus", "keeper/ksm_config"); err == nil {
		t.Fatal("a pending approval stored the value")
	}
	if code, out = g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != http.StatusOK {
		t.Fatalf("the owner's approval: %d %s", code, out)
	}
	// The approval is for this value: another one is refused.
	code, out = g.call(t, apiKey, "POST", path, `{"value":"another","approval_id":"`+id+`"}`)
	if code == http.StatusOK {
		t.Fatalf("another value under the approval: %d %s", code, out)
	}
	code, out = g.call(t, apiKey, "POST", path, `{"value":"`+value+`","approval_id":"`+id+`"}`)
	var res admin.SecretSetResult
	if code != http.StatusOK || json.Unmarshal([]byte(out), &res) != nil || !res.Success || res.Name != "keeper/ksm_config" || res.Length != len(value) || res.Existed {
		t.Fatalf("the approved write: %d %s", code, out)
	}
	if got, err := keyring.Get("cerberus", "keeper/ksm_config"); err != nil || got != value {
		t.Fatalf("the keyring entry (service cerberus, account keeper/ksm_config): %q %v", got, err)
	}
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

// Under the baseline policy an admin key sets a secret straight away, and setting it again
// reports that a value was replaced. Observe mode has no admin lane: 503 admin_unavailable.
func TestSecretSetUnderTheBaselineAndInObserveMode(t *testing.T) {
	g := secretsAPI(t, gatetest.Options{})
	for i, want := range []string{`"existed":false`, `"existed":true`} {
		code, out := g.call(t, apiKey, "POST", "/api/secrets/onepassword/service_account_token", `{"value":"tok-`+string(rune('a'+i))+`"}`)
		if code != http.StatusOK || !strings.Contains(out, want) {
			t.Fatalf("set %d: %d %s", i, code, out)
		}
	}
	if got, _ := keyring.Get("cerberus", "onepassword/service_account_token"); got != "tok-b" {
		t.Fatalf("keyring = %q", got)
	}
	a := newTestAPI(t, "")
	code, out := (&gatedAPI{h: a.Mux()}).call(t, apiKey, "POST", "/api/secrets/keeper/ksm_config", `{"value":"v"}`)
	wantCode(t, "observe mode", code, out, http.StatusServiceUnavailable, "admin_unavailable")
}
