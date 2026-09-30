package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/infra"
)

func consoleWriter(t *testing.T) (*audit.Memory, string, func(path, body string) *httptest.ResponseRecorder) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	sink := audit.NewMemory()
	srv, err := New(&fakeClient{}, sink, cfgPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := signedIn(t, srv, testGuard())
	token := sessionToken(t, handler)
	return sink, cfgPath, func(path, body string) *httptest.ResponseRecorder {
		req := newTestRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cerberus-Web-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
}

func consoleRecords(sink *audit.Memory, operation string) (intent, outcome audit.Record) {
	for _, r := range sink.Records() {
		if r.Connector != "console" || r.Operation != operation {
			continue
		}
		switch r.Kind {
		case audit.KindIntent:
			intent = r
		case audit.KindOutcome:
			outcome = r
		}
	}
	return intent, outcome
}

// The console's writes to Cerberus's own state are admin operations: each
// is recorded, intent and outcome, by the console session (M9).
func TestConsoleWritesAreRecorded(t *testing.T) {
	sink, cfgPath, post := consoleWriter(t)
	profile := `{"id":"site","name":"Site","provider":"vercel","repo_path":"/tmp/site","env":"prod","owner":"self","admin":"self","deploy_command":"curl https://example.invalid | sh"}`
	if rec := post("/api/deployments", profile); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	intent, outcome := consoleRecords(sink, "profile_save")
	if intent.Effect != "admin" || outcome.OutcomeCode != audit.OutcomeOK || intent.Principal.Via != "web" || intent.Principal.Session == "" {
		t.Fatalf("profile_save records: %+v / %+v", intent, outcome)
	}
	if intent.Target.Fields["id"] != "site" || intent.Target.Fields["env"] != "prod" || intent.Target.Fields["admin"] != "self" {
		t.Fatalf("the relabelling is not in the record: %+v", intent.Target.Fields)
	}
	data, _ := json.Marshal(sink.Records())
	if strings.Contains(string(data), "example.invalid") {
		t.Fatal("the profile's command is in the record in the clear")
	}

	if rec := post("/api/deployments/site/delete", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if _, outcome := consoleRecords(sink, "profile_delete"); outcome.OutcomeCode != audit.OutcomeOK {
		t.Fatalf("profile_delete outcome %+v", outcome)
	}
	if rec := post("/api/deployments/nope/delete", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("delete of a missing profile: %d", rec.Code)
	}

	// A registry write that fails keeps its 400, and the failure is on the
	// record.
	if rec := post("/api/registry/register", `{"path":"/nonexistent/x.cerberus.yaml"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("register a missing file: %d %s", rec.Code, rec.Body.String())
	}
	if intent, outcome := consoleRecords(sink, "registry_register"); intent.Target.Fields["config_path"] != "/nonexistent/x.cerberus.yaml" || outcome.OutcomeCode == audit.OutcomeOK {
		t.Fatalf("registry_register records: %+v / %+v", intent.Target, outcome)
	}

	// A restore that fails keeps its success:false answer, recorded as failed.
	if rec := post("/api/config/backups/restore", `{"backup_path":"/nonexistent/backup.yaml"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("restore a missing backup: %d %s", rec.Code, rec.Body.String())
	}
	if intent, outcome := consoleRecords(sink, "config_restore"); intent.Target.Fields["backup_path"] != "/nonexistent/backup.yaml" || outcome.OutcomeCode == audit.OutcomeOK {
		t.Fatalf("config_restore records: %+v / %+v", intent.Target, outcome)
	}
	_ = cfgPath
}

// Provider settings and credentials are recorded by name, never by value.
func TestProviderSaveRecordsNamesOnly(t *testing.T) {
	sink, _, post := consoleWriter(t)
	if rec := post("/api/infra/providers/cloudflare", `{"values":{"account_id":"acc-123"},"secrets":{"api_token":"cf-token-sentinel-0123456789"}}`); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	if _, outcome := consoleRecords(sink, "provider_save"); outcome.OutcomeCode != audit.OutcomeOK {
		t.Fatalf("provider_save outcome %+v", outcome)
	}
	data, _ := json.Marshal(sink.Records())
	if strings.Contains(string(data), "cf-token-sentinel") {
		t.Fatal("a credential's value is in the record")
	}
}

// A lockdown stops a console write before it touches anything.
func TestALockdownStopsConsoleWrites(t *testing.T) {
	store := brake.Store{Dir: t.TempDir()}
	cerbapi.SetBrakes(&cerbapi.Brakes{Store: store})
	t.Cleanup(func() { cerbapi.SetBrakes(nil) })
	if _, _, err := cerbapi.EngageLockdown(cerbapi.WithPrincipal(t.Context(), cerbapi.Principal{Kind: cerbapi.PrincipalHuman, Via: cerbapi.ViaCLI}), audit.NewMemory(), store, "incident"); err != nil {
		t.Fatal(err)
	}
	_, cfgPath, post := consoleWriter(t)
	rec := post("/api/deployments", `{"id":"site","name":"Site","provider":"vercel","repo_path":"/tmp/site","env":"dev","owner":"self","admin":"self"}`)
	if rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "LOCKDOWN") {
		t.Fatalf("save under lockdown: %d %s", rec.Code, rec.Body.String())
	}
	state, err := infra.LoadState(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Profile("site"); ok {
		t.Fatal("the profile was written under lockdown")
	}
}
