package admin_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/admin/admintest"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/gate"
)

// processBackends are run-secrets' backends over root, recording into sink.
func processBackends(t *testing.T, root string, sink audit.Sink) *admin.ProcessBackends {
	t.Helper()
	b, err := admin.NewProcessBackends(admin.ProcessBackendsOptions{
		Gate:        gate.New(gate.Options{Sink: sink}),
		Paths:       admin.Paths{AdoptRoot: root, ConfigDir: filepath.Join(t.TempDir(), "config")},
		HostVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close(t.Context()) })
	return b
}

// run-secrets' backends: the state file's plugins registered, none loaded, the one a reference
// needs loaded on demand in this process and resolved through (recorded as automation via
// run_secrets), the state file untouched, and everything stopped by Close.
func TestProcessBackendsLoadOnDemandAndStop(t *testing.T) {
	root := t.TempDir()
	if _, err := admintest.Install(root, echoBinary, admintest.Plugin{ID: "vault", Loaded: true, SecretBackend: "op"}); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "plugin-connectors.json")
	before, err := os.ReadFile(statePath) //nolint:gosec // the test's own TempDir
	if err != nil {
		t.Fatal(err)
	}
	sink := audit.NewMemory()
	backends := processBackends(t, root, sink)
	if !backends.Claims("op") || backends.Claims("keeper") || backends.Loaded("vault") {
		t.Fatalf("claims op %v keeper %v, loaded %v; want op claimed and nothing loaded", backends.Claims("op"), backends.Claims("keeper"), backends.Loaded("vault"))
	}
	if got := backends.Backend("op"); got != "vault@1.0.0" {
		t.Fatalf("Backend = %q", got)
	}
	if n := len(sink.Records()); n != 0 {
		t.Fatalf("registering wrote %d audit records", n)
	}

	value, err := backends.ResolveSecret(t.Context(), "op://Deploy/Database/password")
	if err != nil || value != "resolved-op:--Deploy-Database-password" {
		t.Fatalf("ResolveSecret = %q, %v", value, err)
	}
	if !backends.Loaded("vault") {
		t.Fatal("the backend is not running after a resolve")
	}
	var intent, outcome audit.Record
	for _, r := range sink.Records() {
		if r.Operation == "load" && r.Principal.Via == "run_secrets" {
			switch r.Kind {
			case audit.KindIntent:
				intent = r
			case audit.KindOutcome:
				outcome = r
			}
		}
	}
	if outcome.Principal.Surface != "run_secrets" || outcome.Principal.Kind != audit.PrincipalAutomation ||
		!strings.Contains(outcome.Reason, "secret backend loaded by run-secrets") || outcome.Decision != audit.DecisionAllowed {
		t.Fatalf("the one-shot load was not recorded as run-secrets: %+v", outcome)
	}
	if intent.OperationID == "" || intent.OperationID != outcome.OperationID {
		t.Fatalf("intent %+v does not pair with outcome %+v", intent, outcome)
	}
	if strings.Contains(string(mustJSON(t, sink.Records())), "resolved-op") {
		t.Fatal("a resolved value reached the audit records")
	}

	// A second reference through the same backend does not load it again.
	if _, err = backends.ResolveSecret(t.Context(), "op://Deploy/Other/password"); err != nil {
		t.Fatal(err)
	}
	loads := 0
	for _, r := range sink.Records() {
		if r.Kind == audit.KindOutcome && r.Operation == "load" {
			loads++
		}
	}
	if loads != 1 {
		t.Fatalf("%d loads recorded, want 1", loads)
	}

	backends.Close(t.Context())
	if backends.Loaded("vault") {
		t.Fatal("Close left the backend running")
	}
	after, err := os.ReadFile(statePath) //nolint:gosec // the test's own TempDir
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("run-secrets' backends wrote the server's state file")
	}
}

// A backend whose install review is pending is not handed secrets by a managed service, and
// nothing is started or recorded.
func TestProcessBackendsRefuseAnUnreviewedBackend(t *testing.T) {
	root := t.TempDir()
	if _, err := admintest.Install(root, echoBinary, admintest.Plugin{ID: "vault", Pending: true, SecretBackend: "op"}); err != nil {
		t.Fatal(err)
	}
	sink := audit.NewMemory()
	backends := processBackends(t, root, sink)
	value, err := backends.ResolveSecret(t.Context(), "op://a/b/c")
	if value != "" || err == nil || !strings.Contains(err.Error(), "install review is pending") || !strings.Contains(err.Error(), "managed review vault") {
		t.Fatalf("ResolveSecret = %q, %v", value, err)
	}
	if backends.Loaded("vault") {
		t.Fatal("an unreviewed backend was started")
	}
	if n := len(sink.Records()); n != 0 {
		t.Fatalf("a refused load wrote %d audit records", n)
	}
	if marks := admintest.Marks(root, "loaded"); len(marks) != 0 {
		t.Fatalf("the plugin ran: %v", marks)
	}
}

// A scheme no installed plugin claims fails as credential_missing, naming the install.
func TestProcessBackendsWithoutAClaimant(t *testing.T) {
	backends := processBackends(t, t.TempDir(), audit.NewMemory())
	_, err := backends.ResolveSecret(t.Context(), "keeper://a/b")
	if err == nil || !strings.Contains(err.Error(), "credential_missing") || !strings.Contains(err.Error(), "keeper://") {
		t.Fatalf("err = %v", err)
	}
}

// The gate is required: a backend is never started unrecorded.
func TestProcessBackendsNeedTheGate(t *testing.T) {
	if _, err := admin.NewProcessBackends(admin.ProcessBackendsOptions{Paths: admin.Paths{AdoptRoot: t.TempDir()}}); err == nil {
		t.Fatal("NewProcessBackends accepted no gate")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
