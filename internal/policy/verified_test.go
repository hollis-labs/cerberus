package policy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// applyVerified applies f as cerbapi.ApplyPolicy does: an intent naming the
// hash before the files are written, and an outcome carrying the snapshot.
func applyVerified(t *testing.T, store Store, sink *audit.FileSink, f File) string {
	t.Helper()
	data, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	hash := Hash(data)
	intent := audit.Record{Kind: audit.KindIntent, OperationID: audit.NewID(), Connector: "policy", Operation: "apply",
		Target: audit.Target{Kind: "policy.snapshot", Fields: map[string]string{"hash": hash}}, Enforcement: f.EnforcementOf().Record()}
	if _, err := sink.Write(intent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(f); err != nil {
		t.Fatal(err)
	}
	outcome := intent
	outcome.Kind, outcome.Decision, outcome.OutcomeCode, outcome.PolicySnapshot = audit.KindOutcome, audit.DecisionAllowed, audit.OutcomeOK, string(data)
	if _, err := sink.Write(outcome); err != nil {
		t.Fatal(err)
	}
	return hash
}

// noZones is a policy an operator wrote: a deny the baseline does not
// have, and scoped enforcement.
var noZones = File{Version: FileVersion,
	Providers:   map[string]Provider{"cloudflare": {Rules: []Rule{{Ops: []string{"create_zone"}, Decision: Deny, Reason: "zones are created by hand"}}}},
	Enforcement: &Enforcement{Enforce: []EnforceEntry{{ID: "agents-prod", Match: TargetMatch{Env: "prod"}, Principal: "agent"}}},
}

func verifiedStore(t *testing.T) (Store, *audit.FileSink) {
	t.Helper()
	auditDir := filepath.Join(t.TempDir(), "audit")
	sink, err := audit.OpenFileSink(auditDir)
	if err != nil {
		t.Fatal(err)
	}
	return Store{Dir: t.TempDir(), AuditDir: auditDir}, sink
}

func zoneDenied(t *testing.T, ev *Evaluator) bool {
	t.Helper()
	got := ev.Authorize(req("cloudflare", "create_zone", contract.EffectWrite, "human", "cloudflare.account", &devService))
	return got.Decision == Deny && got.Reason() == "zones are created by hand"
}

// Applies the log vouches for load as they are.
func TestAVerifiedApplyLoadsAsItIs(t *testing.T) {
	store, sink := verifiedStore(t)
	applyVerified(t, store, sink, File{Version: FileVersion})
	hash := applyVerified(t, store, sink, noZones)
	ev, status := store.LoadVerified()
	if status.Mismatch() || status.Snapshot != hash || !zoneDenied(t, ev) {
		t.Fatalf("status %+v", status)
	}
}

// The snapshot's hash file vouches only for itself: a snapshot rewritten
// with a matching hash file, or deleted, is not the one the log's last
// apply wrote, and the last verified snapshot is enforced — its deny and
// its enforcement, not only the enforcement (M3).
func TestAMismatchKeepsTheLastVerifiedRules(t *testing.T) {
	for name, tamper := range map[string]func(t *testing.T, store Store){
		"rewritten with its hash": func(t *testing.T, store Store) {
			if _, err := store.Apply(File{Version: FileVersion}); err != nil {
				t.Fatal(err)
			}
		},
		"deleted": func(t *testing.T, store Store) {
			for _, name := range []string{appliedName, hashName} {
				if err := os.Remove(filepath.Join(store.Dir, name)); err != nil {
					t.Fatal(err)
				}
			}
		},
		"failing its hash check": func(t *testing.T, store Store) { tamperFile(t, filepath.Join(store.Dir, appliedName)) },
	} {
		t.Run(name, func(t *testing.T) {
			store, sink := verifiedStore(t)
			hash := applyVerified(t, store, sink, noZones)
			tamper(t, store)
			for i := 0; i < 2; i++ { // a fresh Reloading is a daemon restart
				r := NewReloading(store, nil)
				ev, status := store.LoadVerified()
				if !status.Mismatch() || status.Recorded != hash && name != "failing its hash check" || !strings.Contains(status.Enforcement, "last verified snapshot") {
					t.Fatalf("status %+v", status)
				}
				if !zoneDenied(t, ev) || len(r.File().Providers) != 1 {
					t.Fatal("the operator's deny did not survive the mismatch")
				}
				if on, by := r.Enforced(enforceReq("prod", "agent", contract.EffectLifecycle)); !on || !strings.Contains(by, "agents-prod") {
					t.Fatalf("enforcement: %v %q", on, by)
				}
				if on, _ := r.Enforced(enforceReq("dev", "human", contract.EffectLifecycle)); on {
					t.Fatal("enforced beyond the verified scopes")
				}
			}
		})
	}
}

// An apply still running has recorded its hash and not yet its outcome:
// its snapshot is not a mismatch.
func TestAnApplyInFlightIsNotAMismatch(t *testing.T) {
	store, sink := verifiedStore(t)
	data, _ := Encode(noZones)
	if _, err := sink.Write(audit.Record{Kind: audit.KindIntent, OperationID: "op1", Connector: "policy", Operation: "apply",
		Target: audit.Target{Fields: map[string]string{"hash": Hash(data)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(noZones); err != nil {
		t.Fatal(err)
	}
	if _, status := store.LoadVerified(); status.Mismatch() {
		t.Fatalf("status %+v", status)
	}
	// A failed apply is passed over.
	if _, err := sink.Write(audit.Record{Kind: audit.KindOutcome, OperationID: "op1", Connector: "policy", Operation: "apply",
		Decision: audit.DecisionAllowed, OutcomeCode: "operation_failed"}); err != nil {
		t.Fatal(err)
	}
	if _, status := store.LoadVerified(); !status.Mismatch() {
		t.Fatal("a failed apply vouched for its snapshot")
	}
}

// Applies recorded only past a break in the chain vouch for nothing:
// everything is enforced, whatever the files say.
func TestApplyRecordsPastABreakVouchForNothing(t *testing.T) {
	store, sink := verifiedStore(t)
	applyVerified(t, store, sink, File{Version: FileVersion})
	breakApplyRecord(t, store.AuditDir)
	ev, status := store.LoadVerified()
	if on, _ := ev.Enforced(enforceReq("dev", "human", contract.EffectLifecycle)); !on || !status.Mismatch() || !strings.Contains(status.Problem, "reanchored") {
		t.Fatalf("enforced %v, status %+v", on, status)
	}
}

// An apply still running is vouched for only by a trusted intent.
func TestAnIntentPastABreakVouchesForNothing(t *testing.T) {
	store, sink := verifiedStore(t)
	data, _ := Encode(noZones)
	if _, err := sink.Write(audit.Record{Kind: audit.KindIntent, OperationID: "op1", Connector: "policy", Operation: "apply",
		Target: audit.Target{Kind: "policy.snapshot", Fields: map[string]string{"hash": Hash(data)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(noZones); err != nil {
		t.Fatal(err)
	}
	breakApplyRecord(t, store.AuditDir)
	if _, status := store.LoadVerified(); !status.Mismatch() {
		t.Fatalf("an untrusted intent vouched for its snapshot: %+v", status)
	}
}

// breakApplyRecord edits the first apply record in the log, as a same-uid
// process could, which breaks the chain there.
func breakApplyRecord(t *testing.T, auditDir string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(auditDir, "*.jsonl"))
	data, err := os.ReadFile(files[0]) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(data, []byte(`"policy.snapshot"`), []byte(`"policy.snapshoT"`), 1)
	if err := os.WriteFile(files[0], edited, 0o600); err != nil { //nolint:gosec // as above
		t.Fatal(err)
	}
}
