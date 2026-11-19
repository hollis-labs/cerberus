package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/hostmon"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

func (w *hostWorld) writer(g *gatetest.World) *hostmon.Writer {
	return &hostmon.Writer{HostsPath: filepath.Join(w.root, "hosts.yaml"), Gate: g.Gate, Prober: w.reads.Prober}
}

func send(t *testing.T, a *API, p libauth.Principal, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(libauth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	a.Mux().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func revisionOf(t *testing.T, a *API) string {
	t.Helper()
	code, body := send(t, a, apiKey, "GET", "/api/hosts/_revision", "")
	var rev struct{ Revision string }
	if code != 200 || json.Unmarshal([]byte(body), &rev) != nil || rev.Revision == "" {
		t.Fatalf("revision: %d %s", code, body)
	}
	return rev.Revision
}

// The host and component writes pass the gate as the host contract's operation and change
// hosts.yaml through internal/hosts' editor, as main's did: a write needs the revision the caller
// read and refuses credential material, writing nothing; an applied change is recorded in the
// audit (intent and outcome) and in hosts.history.jsonl with its caller; a stale revision
// conflicts; an engaged lockdown refuses a write; observe mode writes nothing.
func TestHostWritesAreGatedAndRecorded(t *testing.T) {
	w := newHostWorld(t)
	g := gatetest.New(t, gatetest.Options{})
	a := w.api(t, g)
	a.o.HostWriter = w.writer(g)
	yamlPath := filepath.Join(w.root, "hosts.yaml")
	unchanged := func(before []byte) {
		t.Helper()
		after, _ := os.ReadFile(yamlPath) //nolint:gosec // the test's own file
		if string(after) != string(before) {
			t.Fatal("hosts.yaml was written")
		}
	}
	before, _ := os.ReadFile(yamlPath) //nolint:gosec // the test's own file

	if code, body := send(t, a, apiKey, "PATCH", "/api/hosts/zeta", `{"acknowledged":true,"patch":{"labels":{"rack":"4"}}}`); code != http.StatusPreconditionRequired || !strings.Contains(body, `"code":"revision_required"`) {
		t.Fatalf("no revision: %d %s", code, body)
	}
	rev := revisionOf(t, a)
	secret := "ghp" + "_" + strings.Repeat("Zq7x", 9)
	if code, body := send(t, a, apiKey, "PATCH", "/api/hosts/zeta", `{"revision":"`+rev+`","acknowledged":true,"patch":{"labels":{"token":"`+secret+`"}}}`); code != 400 || strings.Contains(body, secret) {
		t.Fatalf("credential material: %d %s", code, body)
	}
	unchanged(before)

	code, body := send(t, a, apiKey, "PATCH", "/api/hosts/zeta", `{"revision":"`+rev+`","acknowledged":true,"patch":{"labels":{"rack":"4"}}}`)
	if code != 200 || !strings.Contains(body, `"changed":true`) || !strings.Contains(body, `"labels.rack":"4"`) || !strings.Contains(body, `"op":"host_update"`) {
		t.Fatalf("update: %d %s", code, body)
	}
	if code, body = send(t, a, apiKey, "PATCH", "/api/hosts/zeta", `{"revision":"`+rev+`","acknowledged":true,"patch":{"labels":{"rack":"5"}}}`); code != 409 || !strings.Contains(body, `"code":"revision_conflict"`) {
		t.Fatalf("stale revision: %d %s", code, body)
	}
	rev = revisionOf(t, a)
	comp := `{"revision":"` + rev + `","acknowledged":true,"component":{"id":"ram-1","kind":"ram","size":"16GiB","location":{"spare":true}}}`
	if code, body = send(t, a, apiKey, "POST", "/api/components", comp); code != 200 || !strings.Contains(body, `"op":"component_create"`) {
		t.Fatalf("component create: %d %s", code, body)
	}
	if code, body = send(t, a, apiKey, "GET", "/api/components?spare=true", ""); code != 200 || !strings.Contains(body, `"id":"ram-1"`) {
		t.Fatalf("component list: %d %s", code, body)
	}
	if code, body = send(t, a, apiKey, "GET", "/api/components/ram-1", ""); code != 200 || !strings.Contains(body, `"kind":"ram"`) {
		t.Fatalf("component get: %d %s", code, body)
	}
	if code, body = send(t, a, apiKey, "GET", "/api/hosts/zeta/history", ""); code != 200 || !strings.Contains(body, `"op":"host_update"`) || !strings.Contains(body, `"caller":"socket/api key:k1"`) {
		t.Fatalf("host history: %d %s", code, body)
	}
	if code, body = send(t, a, apiKey, "GET", "/api/components/ram-1/history", ""); code != 200 || !strings.Contains(body, `"op":"component_create"`) {
		t.Fatalf("component history: %d %s", code, body)
	}
	recorded := ""
	for _, r := range g.Records(audit.KindOutcome) {
		recorded += " " + r.Connector + "." + r.Operation
	}
	for _, op := range []string{"host.host_update", "host.component_create", "host.host_history", "host.component_list"} {
		if !strings.Contains(recorded, op) {
			t.Errorf("%s has no outcome recorded: %s", op, recorded)
		}
	}

	g.Engage(t, brakeLockdown())
	rev = revisionOf(t, a)
	before, _ = os.ReadFile(yamlPath) //nolint:gosec // the test's own file
	if code, body = send(t, a, apiKey, "DELETE", "/api/components/ram-1", `{"revision":"`+rev+`","acknowledged":true}`); code != http.StatusLocked || !strings.Contains(body, `"code":"lockdown"`) {
		t.Fatalf("a write under lockdown: %d %s", code, body)
	}
	unchanged(before)

	observe := w.api(t, nil)
	observe.o.HostWriter = &hostmon.Writer{HostsPath: yamlPath, Prober: w.reads.Prober}
	if code, body = send(t, observe, apiKey, "PATCH", "/api/hosts/zeta", `{"revision":"`+rev+`","acknowledged":true,"patch":{"labels":{"rack":"6"}}}`); code != http.StatusConflict || !strings.Contains(body, `"code":"observe_mode"`) {
		t.Fatalf("a write in observe mode: %d %s", code, body)
	}
	unchanged(before)
	if _, err := os.Stat(filepath.Join(w.root, "hosts.history.jsonl")); err != nil {
		t.Fatalf("no history file beside hosts.yaml: %v", err)
	}
	_ = hosts.File{}
}

func brakeLockdown() brake.State {
	return brake.State{Lockdown: &brake.Lockdown{ID: "ldn_w", EngagedAt: time.Now(), Reason: "drill"}}
}
