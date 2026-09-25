package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"

	"github.com/hollis-labs/cerberus/internal/target"
)

func adhocReq(connector, op string, effect contract.Effect, kind string) Request {
	return Request{Connector: connector, Operation: op, Effect: effect, Principal: Principal{Kind: kind},
		Target: target.Resolve(connector, "ssh.host", "", nil, true)}
}

// The two layers (section 13, settled at the P2-5 review): the global
// posture is where evaluation starts; a secure rule wins over a permissive
// one; a permissive rule reaches only labeled, registered targets, however
// broad its match.
func TestPostureForScopesStrictly(t *testing.T) {
	notProd := PostureRule{Match: TargetMatch{Env: "!prod"}, Posture: PosturePermissive}
	dev := req("local", "deploy", contract.EffectLifecycle, "agent", "local.resource", &devService)
	unlabeled := req("ssh", "exec", contract.EffectExec, "agent", "ssh.host", &unlabeledRes)
	adhoc := adhocReq("ssh", "exec", contract.EffectExec, "agent")
	for _, c := range []struct {
		name string
		file File
		r    Request
		want string
	}{
		{"nothing said is secure", File{}, dev, PostureSecure},
		{"the global posture applies", File{Posture: PosturePermissive}, unlabeled, PosturePermissive},
		{"a permissive rule reaches a labeled target", File{PostureRules: []PostureRule{notProd}}, dev, PosturePermissive},
		{"a permissive rule never reaches an unlabeled target", File{PostureRules: []PostureRule{notProd}}, unlabeled, PostureSecure},
		{"a permissive rule never reaches an ad-hoc target", File{PostureRules: []PostureRule{notProd}}, adhoc, PostureSecure},
		{"a secure rule wins over a permissive one", File{PostureRules: []PostureRule{notProd, {Match: TargetMatch{ID: "notes-api"}, Posture: PostureSecure}}}, dev, PostureSecure},
		{"a secure rule narrows a global permissive", File{Posture: PosturePermissive, PostureRules: []PostureRule{{Match: TargetMatch{Env: "dev"}, Posture: PostureSecure}}}, dev, PostureSecure},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got, _ := c.file.PostureFor(c.r); got != c.want {
				t.Fatalf("posture = %s, want %s", got, c.want)
			}
			if res := NewEvaluator(c.file, "test").Authorize(c.r); res.Posture != c.want {
				t.Fatalf("Result.Posture = %s, want %s", res.Posture, c.want)
			}
		})
	}
}

// Under permissive the built-in strictness steps aside, and the operator's
// own rules, and a target labeled as someone else's to administer, do not.
func TestPermissiveRelaxesTheBuiltInsOnly(t *testing.T) {
	open := File{Posture: PosturePermissive}
	for _, c := range []struct {
		name string
		file File
		r    Request
		want Decision
		rule string
	}{
		{"an agent's write on a dev target is allowed", open, req("local", "deploy", contract.EffectLifecycle, "agent", "local.resource", &devService), Allow, "posture.permissive"},
		{"an unlabeled target is not read as prod or the owner's", open, req("ssh", "exec", contract.EffectExec, "agent", "ssh.host", &unlabeledRes), Allow, "posture.permissive"},
		{"an ad-hoc target needs no grant", open, adhocReq("ssh", "exec", contract.EffectExec, "agent"), Allow, "posture.permissive"},
		{"a target the owner administers is still denied", open, req("kubernetes", "scale_workload", contract.EffectLifecycle, "human", "kubernetes.workload", &stageHost), Deny, "builtin.admin-owner"},
		{"an operator's deny still applies", File{Posture: PosturePermissive, Targets: []TargetBlock{{Match: TargetMatch{ID: "notes-api"},
			Rules: []Rule{{Effect: []contract.Effect{contract.EffectLifecycle}, Decision: Deny, Reason: "frozen"}}}}},
			req("local", "deploy", contract.EffectLifecycle, "agent", "local.resource", &devService), Deny, "targets[0].rules[0]"},
		{"an operator's baseline cell still applies", File{Posture: PosturePermissive, Baseline: &BaselineOverride{ByEffect: map[contract.Effect]map[string]Decision{
			contract.EffectLifecycle: {"agent": Approve}}}},
			req("local", "deploy", contract.EffectLifecycle, "agent", "local.resource", &devService), Approve, "baseline.lifecycle.agent"},
		{"an unknown effect is still denied", open, req("plugin", "mystery", "", "human", "x", &devService), Deny, "baseline.unknown-effect"},
		{"a scoped permissive relaxes only its targets", File{PostureRules: []PostureRule{{Match: TargetMatch{Env: "dev"}, Posture: PosturePermissive}}},
			req("ssh", "exec", contract.EffectExec, "agent", "ssh.host", &unlabeledRes), Deny, "builtin.admin-unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := NewEvaluator(c.file, "test").Authorize(c.r)
			if got.Decision != c.want || !hasRule(got, c.rule) {
				t.Fatalf("got %s with %+v, want %s from %s", got.Decision, got.Matched, c.want, c.rule)
			}
		})
	}
}

// A plugin operation with no declared effect is exec under secure and write
// under permissive, which is what an operator's effect-scoped rule sees.
func TestUndeclaredPluginEffectFollowsThePosture(t *testing.T) {
	denyExec := []TargetBlock{{Match: TargetMatch{ID: "notes-api"}, Rules: []Rule{{Effect: []contract.Effect{contract.EffectExec}, Decision: Deny, Reason: "no exec here"}}}}
	r := req("kubernetes", "mystery", contract.EffectExec, "agent", "local.resource", &devService)
	r.EffectUndeclared = true
	if got := NewEvaluator(File{Targets: denyExec}, "test").Authorize(r); got.Decision != Deny || !hasRule(got, "targets[0].rules[0]") {
		t.Fatalf("secure: got %s with %+v, want the exec rule's deny", got.Decision, got.Matched)
	}
	if got := NewEvaluator(File{Posture: PosturePermissive, Targets: denyExec}, "test").Authorize(r); got.Decision != Allow || hasRule(got, "targets[0].rules[0]") {
		t.Fatalf("permissive: got %s with %+v, want allow with the exec rule unmatched", got.Decision, got.Matched)
	}
	r.EffectUndeclared = false
	if got := NewEvaluator(File{Posture: PosturePermissive, Targets: denyExec}, "test").Authorize(r); got.Decision != Deny {
		t.Fatalf("a declared exec under permissive: got %s, want the exec rule's deny", got.Decision)
	}
}

func TestPostureSummary(t *testing.T) {
	for _, c := range []struct {
		file       File
		want       string
		permissive bool
	}{
		{File{}, "secure", false},
		{File{Posture: PosturePermissive}, "permissive", true},
		{File{PostureRules: []PostureRule{{Match: TargetMatch{Env: "dev"}, Posture: PosturePermissive}, {Match: TargetMatch{Env: "lab", Owner: "self"}, Posture: PosturePermissive}}},
			"secure; permissive for env=dev and env=lab owner=self (labeled targets only)", true},
		{File{Posture: PosturePermissive, PostureRules: []PostureRule{{Match: TargetMatch{Env: "prod"}, Posture: PostureSecure}}}, "permissive; secure for env=prod", true},
	} {
		s := c.file.PostureSummary("h")
		if s.String() != c.want || s.Permissive() != c.permissive {
			t.Errorf("summary = %q (permissive %t), want %q (%t)", s.String(), s.Permissive(), c.want, c.permissive)
		}
	}
	if !strings.Contains(TargetMatch{}.String(), "every target") {
		t.Fatal("an empty match should read as every target")
	}
}

// The posture every surface shows is the applied snapshot's, checked against
// its hash: nothing applied, or a snapshot that fails its check, is secure.
func TestCurrentPostureIsTheAppliedSnapshots(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if got := store.CurrentPosture(); got.String() != PostureSecure || got.Snapshot != SnapshotBaseline {
		t.Fatalf("nothing applied: %+v", got)
	}
	if _, err := store.Apply(File{Version: FileVersion, Posture: PosturePermissive}); err != nil {
		t.Fatal(err)
	}
	if got := store.CurrentPosture(); got.Global != PosturePermissive {
		t.Fatalf("applied permissive: %+v", got)
	}
	tamper(t, store)
	if got := store.CurrentPosture(); got.Global != PostureSecure || got.Snapshot != SnapshotMismatch {
		t.Fatalf("a snapshot that fails its hash check must read as secure: %+v", got)
	}
}

// tamper edits the applied snapshot behind policy apply's back.
func tamper(t *testing.T, store Store) {
	t.Helper()
	path := filepath.Join(store.Dir, appliedName)
	data, err := os.ReadFile(path) //nolint:gosec // the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(data, []byte("\n# edited by hand\n")...), 0o600); err != nil { //nolint:gosec // the test's own temp dir
		t.Fatal(err)
	}
}
