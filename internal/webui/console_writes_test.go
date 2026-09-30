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
)

func consoleWriter(t *testing.T) (*audit.Memory, *memorySecrets, func(path, body string) *httptest.ResponseRecorder) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	sink := audit.NewMemory()
	store := &memorySecrets{values: map[string]string{}}
	srv, err := New(&fakeClient{consoleWrites: consoleDaemon(cfgPath, sink, store)}, sink, cfgPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := signedIn(t, srv, testGuard())
	token := sessionToken(t, handler)
	return sink, store, func(path, body string) *httptest.ResponseRecorder {
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
	sink, _, post := consoleWriter(t)
	if rec := post("/api/credentials/cloudflare", `{"secrets":{"api_token":"cf-token-sentinel-0123456789"}}`); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	intent, outcome := consoleRecords(sink, "provider_save")
	if intent.Effect != "admin" || outcome.OutcomeCode != audit.OutcomeOK || intent.Principal.Via != "web" || intent.Principal.Session == "" {
		t.Fatalf("provider_save records: %+v / %+v", intent, outcome)
	}
	if intent.Target.Fields["id"] != "cloudflare" {
		t.Fatalf("the connector is not in the record: %+v", intent.Target.Fields)
	}
	// An undeclared key is refused with 400, and nothing is written.
	if rec := post("/api/credentials/cloudflare", `{"secrets":{"password":"x"}}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not declare") {
		t.Fatalf("an undeclared key: %d %s", rec.Code, rec.Body.String())
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
}

// Credentials are recorded by name, never by value.
func TestProviderSaveRecordsNamesOnly(t *testing.T) {
	sink, _, post := consoleWriter(t)
	if rec := post("/api/credentials/cloudflare", `{"secrets":{"api_token":"cf-token-sentinel-0123456789"}}`); rec.Code != http.StatusOK {
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
	_, secrets, post := consoleWriter(t)
	rec := post("/api/credentials/cloudflare", `{"secrets":{"api_token":"under-lockdown"}}`)
	if rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "LOCKDOWN") {
		t.Fatalf("save under lockdown: %d %s", rec.Code, rec.Body.String())
	}
	if len(secrets.values) != 0 {
		t.Fatalf("a credential was written under lockdown: %v", secrets.values)
	}
}
