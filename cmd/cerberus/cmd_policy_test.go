package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func policyFixture(t *testing.T, terminal bool) (policy.Store, *audit.Memory) {
	t.Helper()
	store := policy.Store{Dir: filepath.Join(t.TempDir(), "policy")}
	sink := audit.NewMemory()
	oldStore, oldTerm, oldDefs, oldRes, oldSink := policyStore, policyIsTerminal, policyDefinitions, policyResources, policySink
	policyStore = func() (policy.Store, error) { return store, nil }
	policyIsTerminal = func() bool { return terminal }
	policyDefinitions = func(context.Context) []contract.Definition { return []contract.Definition{localconn.Definition()} }
	policyResources = func() []policy.SampleTarget {
		return []policy.SampleTarget{{Connector: "local", Labels: target.ResourceLabels{ID: "notes-api",
			Labels: target.Labels{Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}}}}}
	}
	policySink = func() audit.Sink { return sink }
	t.Cleanup(func() {
		policyStore, policyIsTerminal, policyDefinitions, policyResources, policySink = oldStore, oldTerm, oldDefs, oldRes, oldSink
		policyExplainFlags.target, policyExplainFlags.as, policyExplainFlags.output = "", "", ""
		policyExplainFlags.dryRun, policyExplainFlags.adhoc, policyExplainFlags.working = false, false, false
	})
	return store, sink
}

func runPolicy(t *testing.T, stdin string, cmdArgs ...string) (string, error) {
	t.Helper()
	// Flag values outlive an Execute; start each run from the defaults.
	policyExplainFlags.target, policyExplainFlags.as, policyExplainFlags.output = "", "", outputFormatText
	policyExplainFlags.dryRun, policyExplainFlags.adhoc, policyExplainFlags.working = false, false, false
	var out bytes.Buffer
	rootCmd.SetArgs(append([]string{"policy"}, cmdArgs...))
	rootCmd.SetOut(&out)
	rootCmd.SetIn(strings.NewReader(stdin))
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetIn(nil) })
	err := rootCmd.Execute()
	return out.String(), err
}

func TestPolicyExplain(t *testing.T) {
	policyFixture(t, false)
	out, err := runPolicy(t, "", "explain", "local.deploy", "--target", "notes-api", "--as", "human")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Decision: allow", "builtin.local-dev-lifecycle", "the built-in baseline", "Shadow mode: nothing is refused today", "Posture:  secure"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain is missing %q:\n%s", want, out)
		}
	}
	out, err = runPolicy(t, "", "explain", "local.deploy", "--as", "agent", "--adhoc")
	if err != nil || !strings.Contains(out, "Decision: deny") || !strings.Contains(out, "builtin.adhoc") || !strings.Contains(out, "builtin.admin-unknown") {
		t.Fatalf("explain ad hoc agent:\n%s (%v)", out, err)
	}
	if _, err := runPolicy(t, "", "explain", "local.deploy", "--target", "ghost"); err == nil {
		t.Fatal("an unknown target was explained")
	}
	if _, err := runPolicy(t, "", "explain", "deploy"); err == nil {
		t.Fatal("a bare operation was accepted")
	}
}

func TestPolicyApply(t *testing.T) {
	t.Run("refused without a terminal", func(t *testing.T) {
		policyFixture(t, false)
		_, err := runPolicy(t, "", "apply")
		if !errors.Is(err, errPolicyApplyNotInteractive) {
			t.Fatalf("err = %v", err)
		}
		if got := redact.Text(err.Error()); got != err.Error() {
			t.Fatalf("redaction rewrote the refusal: %q", got)
		}
	})
	t.Run("shows the flips and applies on the typed confirmation", func(t *testing.T) {
		store, sink := policyFixture(t, true)
		if err := os.MkdirAll(store.Dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store.Dir, "main.yaml"), []byte("version: 1\nproviders:\n  local:\n    rules:\n      - {id: no-remove, ops: [remove], decision: deny}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		working, _, _ := store.LoadWorking()
		data, _ := policy.Encode(working)
		phrase := "apply " + shortHash(policy.Hash(data))

		out, err := runPolicy(t, "yes\n", "apply")
		if err == nil || !strings.Contains(out, "local.remove") || !strings.Contains(out, "no-remove") {
			t.Fatalf("a wrong confirmation: %v\n%s", err, out)
		}
		if _, st := store.Load(); st.Snapshot != policy.SnapshotBaseline {
			t.Fatal("a wrong confirmation applied")
		}
		out, err = runPolicy(t, phrase+"\n", "apply")
		if err != nil || !strings.Contains(out, "Applied sha256:") {
			t.Fatalf("apply: %v\n%s", err, out)
		}
		if _, st := store.Load(); st.Snapshot != policy.Hash(data) {
			t.Fatalf("applied %+v", st)
		}
		recs := sink.Records()
		if len(recs) != 2 || recs[0].Connector != "policy" || recs[0].Operation != "apply" || recs[0].Effect != "admin" || recs[1].OutcomeCode != audit.OutcomeOK {
			t.Fatalf("records %+v", recs)
		}
		out, err = runPolicy(t, "", "apply")
		if err != nil || !strings.Contains(out, "nothing to apply") {
			t.Fatalf("second apply: %v %s", err, out)
		}
	})
	t.Run("invalid working files apply nothing", func(t *testing.T) {
		store, _ := policyFixture(t, true)
		_ = os.MkdirAll(store.Dir, 0o700)
		_ = os.WriteFile(filepath.Join(store.Dir, "main.yaml"), []byte("version: 1\nposture: wide-open\n"), 0o600)
		if _, err := runPolicy(t, "", "apply"); err == nil || !strings.Contains(err.Error(), "posture") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestPolicyReportGroupsWouldBlock(t *testing.T) {
	block := func(op, kind, rule string) audit.Record {
		return audit.Record{Kind: audit.KindIntent, Connector: "local", Operation: op, Principal: audit.Principal{Kind: kind},
			Target: audit.Target{Resource: "notes-api", Env: "dev"},
			Policy: &audit.PolicyDecision{Decision: "approve", WouldBlock: true, MatchedRules: []audit.MatchedRule{{Rule: rule, Decision: "approve"}}}}
	}
	allow := block("deploy", "human", "x")
	allow.Policy = &audit.PolicyDecision{Decision: "allow"}
	recs := []audit.Record{block("stop", "agent", "baseline.lifecycle.agent"), block("stop", "agent", "baseline.lifecycle.agent"), block("remove", "human", "baseline.destructive.human"), allow,
		{Kind: audit.KindOutcome, Policy: &audit.PolicyDecision{WouldBlock: true}}}
	rows, total, decided := policyReport(recs, zeroTime, zeroTime)
	if total != 3 || decided != 4 || len(rows) != 2 || rows[0].Count != 2 || rows[0].Operation != "local.stop" || rows[0].Rule != "baseline.lifecycle.agent" {
		t.Fatalf("rows %+v total %d decided %d", rows, total, decided)
	}
	_ = cerbapi.SurfaceInProcess
}

var zeroTime = func() (t time.Time) { return }()
