package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"

	"github.com/hollis-labs/cerberus/internal/target"
)

// Fixtures shaped like a labeled estate: a local dev service, a host
// another team provisions where we run the containers and software, a host
// administered jointly, and one nobody labeled.
var (
	devService = target.ResourceLabels{ID: "notes-api", Labels: target.Labels{Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}}}
	stageHost  = target.ResourceLabels{ID: "stage-box", Labels: target.Labels{Env: target.EnvWork, Owner: "platform-team",
		Admin: target.Admin{Default: target.AdminOwner, ByKind: map[string]string{"docker": target.AdminSelf, "software": target.AdminSelf, "ssh": target.AdminSelf}}}}
	sharedHost = target.ResourceLabels{ID: "gateway-box", Labels: target.Labels{Env: target.EnvWork, Owner: "network-team",
		Admin: target.Admin{Default: target.AdminShared, ByKind: map[string]string{"docker": target.AdminSelf, "ssh": target.AdminSelf}}}}
	unlabeledRes = target.ResourceLabels{ID: "mystery"}
)

func req(connector, op string, effect contract.Effect, kind, targetKind string, res *target.ResourceLabels) Request {
	return Request{Connector: connector, Operation: op, Effect: effect, Principal: Principal{Kind: kind},
		Target: target.Resolve(connector, targetKind, "", res, false)}
}

func TestBaselineAndTargetDefaults(t *testing.T) {
	pdp := BaselineOnly(SnapshotBaseline)
	for _, c := range []struct {
		name string
		r    Request
		want Decision
		rule string
	}{
		{"human lifecycle on local dev is allowed (Decision 11)", req("local", "deploy", contract.EffectLifecycle, "human", "local.resource", &devService), Allow, "builtin.local-dev-lifecycle"},
		{"an agent's lifecycle on local dev needs approval", req("local", "deploy", contract.EffectLifecycle, "agent", "local.resource", &devService), Approve, "baseline.lifecycle.agent"},
		{"a human's destructive op needs approval", req("local", "remove", contract.EffectDestructive, "human", "local.resource", &devService), Approve, "baseline.destructive.human"},
		{"a read is allowed for an agent", req("docker", "list_containers", contract.EffectRead, "agent", "docker.daemon", &stageHost), Allow, "baseline.read.agent"},
		{"read_sensitive needs approval for an agent", req("docker", "logs", contract.EffectReadSensitive, "agent", "docker.container", &stageHost), Approve, "baseline.read_sensitive.agent"},
		{"docker on a host where we administer docker follows the baseline", req("docker", "up", contract.EffectLifecycle, "human", "docker.container", &stageHost), Approve, "baseline.lifecycle.human"},
		{"a kind the owner administers is denied (Decision 18)", req("kubernetes", "scale_workload", contract.EffectLifecycle, "human", "kubernetes.workload", &stageHost), Deny, "builtin.admin-owner"},
		{"destructive on a shared target needs approval", req("forge", "delete_site", contract.EffectDestructive, "human", "forge.site", &sharedHost), Approve, "builtin.admin-shared"},
		{"an unlabeledRes target is the owner's, and denied (Decision 17)", req("ssh", "exec", contract.EffectExec, "human", "ssh.host", &unlabeledRes), Deny, "builtin.admin-unknown"},
		{"an unknown effect is denied", req("plugin", "mystery", "", "human", "x", &devService), Deny, "baseline.unknown-effect"},
		{"admin is the operator's alone", req("plugin", "install", contract.EffectAdmin, "agent", "plugin", nil), Deny, "baseline.admin.agent"},
		{"automation takes the agent column", req("local", "deploy", contract.EffectLifecycle, "automation", "local.resource", &devService), Approve, "baseline.lifecycle.automation"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := pdp.Authorize(c.r)
			if got.Decision != c.want || !hasRule(got, c.rule) {
				t.Fatalf("got %s with %+v, want %s from %s", got.Decision, got.Matched, c.want, c.rule)
			}
			if got.WouldBlock != (c.want != Allow) {
				t.Fatalf("would_block = %t for %s", got.WouldBlock, got.Decision)
			}
		})
	}
	// Unknown env reads as prod: a write needs approval there even where the
	// admin is ours.
	unknownEnv := target.ResourceLabels{ID: "x", Labels: target.Labels{Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}}}
	got := pdp.Authorize(req("local", "sync", contract.EffectWrite, "human", "local.resource", &unknownEnv))
	if !hasRule(got, "builtin.env-unknown") {
		t.Fatalf("unknown env: %+v", got.Matched)
	}
	prodDestroy := target.ResourceLabels{ID: "p", Labels: target.Labels{Env: target.EnvProd, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}}}
	got = pdp.Authorize(req("digitalocean", "destroy", contract.EffectDestructive, "human", "digitalocean.droplet", &prodDestroy))
	var outOfBand bool
	for _, m := range got.Matched {
		outOfBand = outOfBand || (m.Rule == "builtin.env-prod" && m.Approval != nil && m.Approval.Channel == "out_of_band")
	}
	if !outOfBand {
		t.Fatalf("prod destructive: %+v", got.Matched)
	}
}

func TestAdhocNeedsAGrant(t *testing.T) {
	r := Request{Connector: "docker", Operation: "up", Effect: contract.EffectLifecycle, Principal: Principal{Kind: "agent"},
		Target: target.Resolve("docker", "docker.container", "x", nil, true)}
	if got := BaselineOnly(SnapshotBaseline).Authorize(r); !hasRule(got, "builtin.adhoc") || got.Decision != Deny {
		t.Fatalf("agent ad hoc: %+v", got)
	}
	r.Principal.Kind = "human"
	if got := BaselineOnly(SnapshotBaseline).Authorize(r); hasRule(got, "builtin.adhoc") {
		t.Fatalf("a human holds adhoc_targets by default: %+v", got.Matched)
	}
	r.Principal = Principal{Kind: "agent", Client: "ci-runner"}
	f := File{Version: FileVersion, Principals: []PrincipalBlock{{Match: PrincipalMatch{Client: "ci-*"}, Grants: []string{"adhoc_targets"}}}}
	if got := NewEvaluator(f, "sha256:x").Authorize(r); hasRule(got, "builtin.adhoc") {
		t.Fatalf("a granted agent: %+v", got.Matched)
	}
}

// The most restrictive match wins, and a dry run is the plan step: only a
// deny stops it.
func TestCombiningAndDryRuns(t *testing.T) {
	f := File{Version: FileVersion,
		Providers:  map[string]Provider{"cloudflare": {Rules: []Rule{{Ops: []string{"create_zone"}, Decision: Deny, Reason: "zones are created by hand"}}}},
		Targets:    []TargetBlock{{Match: TargetMatch{Env: "!dev"}, Rules: []Rule{{ID: "no-agent-writes", Effect: []contract.Effect{contract.EffectWrite}, Principal: &PrincipalMatch{Kind: "agent"}, Decision: DryRunOnly}}}},
		Principals: []PrincipalBlock{{Match: PrincipalMatch{Kind: "agent"}, Rules: []Rule{{Effect: []contract.Effect{contract.EffectRead}, Decision: Allow}}}},
	}
	pdp := NewEvaluator(f, "sha256:abc")
	zone := req("cloudflare", "create_zone", contract.EffectWrite, "human", "cloudflare.account", &devService)
	got := pdp.Authorize(zone)
	if got.Decision != Deny || got.Reason() != "zones are created by hand" || got.Snapshot != "sha256:abc" {
		t.Fatalf("provider deny: %+v", got)
	}
	zone.DryRun = true
	if dry := pdp.Authorize(zone); !dry.WouldBlock {
		t.Fatal("a deny passed a dry run")
	}
	write := req("docker", "put", contract.EffectWrite, "agent", "docker.container", &stageHost)
	got = pdp.Authorize(write)
	if !hasRule(got, "no-agent-writes") || got.Decision != Approve {
		t.Fatalf("approve outranks dry_run_only: %+v", got)
	}
	write.DryRun = true
	if got := pdp.Authorize(write); got.WouldBlock {
		t.Fatalf("an approval stopped a dry run: %+v", got)
	}
	// An allow never lowers a stricter match; only the baseline loosens.
	loose := File{Version: FileVersion, Targets: []TargetBlock{{Match: TargetMatch{}, Rules: []Rule{{Decision: Allow}}}}}
	if got := NewEvaluator(loose, "x").Authorize(req("local", "remove", contract.EffectDestructive, "human", "local.resource", &devService)); got.Decision != Approve {
		t.Fatalf("an allow rule lowered the baseline: %s", got.Decision)
	}
	override := File{Version: FileVersion, Baseline: &BaselineOverride{ByEffect: map[contract.Effect]map[string]Decision{contract.EffectWrite: {"human": Allow}}}}
	if got := NewEvaluator(override, "x").Authorize(req("local", "sync", contract.EffectWrite, "human", "local.resource", &devService)); got.Decision != Allow {
		t.Fatalf("a baseline override did not loosen: %+v", got)
	}
}

func TestFileValidation(t *testing.T) {
	bad := File{Version: 2,
		Baseline:   &BaselineOverride{ByEffect: map[contract.Effect]map[string]Decision{"writes": {"robot": "maybe"}}},
		Targets:    []TargetBlock{{Rules: []Rule{{Effect: []contract.Effect{"nope"}, Decision: "allowed"}}}},
		Principals: []PrincipalBlock{{Match: PrincipalMatch{Kind: "root"}, Grants: []string{"everything"}}},
	}
	if problems := bad.Validate(); len(problems) != 8 {
		t.Fatalf("problems (%d): %s", len(problems), strings.Join(problems, "\n"))
	}
}

// The decision point uses only an applied snapshot that matches its hash; a
// tampered one falls back to the baseline and says so (D6).
func TestStoreAppliesAndChecksTheSnapshot(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if _, st := s.Load(); st.Snapshot != SnapshotBaseline {
		t.Fatalf("nothing applied: %+v", st)
	}
	f := File{Version: FileVersion, Providers: map[string]Provider{"cloudflare": {Rules: []Rule{{Ops: []string{"create_zone"}, Decision: Deny}}}}}
	hash, err := s.Apply(f)
	if err != nil {
		t.Fatal(err)
	}
	pdp, st := s.Load()
	if st.Snapshot != hash || st.Mismatch() {
		t.Fatalf("applied: %+v", st)
	}
	if got := pdp.Authorize(req("cloudflare", "create_zone", contract.EffectWrite, "human", "x", &devService)); got.Decision != Deny || got.Snapshot != hash {
		t.Fatalf("the applied snapshot was not used: %+v", got)
	}
	// Tampering: a loosened snapshot is not used.
	if err := os.WriteFile(filepath.Join(s.Dir, "applied.yaml"), []byte("version: 1\nbaseline:\n  by_effect:\n    destructive: {agent: allow}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pdp, st = s.Load()
	if !st.Mismatch() || st.Recorded != hash || st.Found == hash {
		t.Fatalf("tampered: %+v", st)
	}
	got := pdp.Authorize(req("local", "remove", contract.EffectDestructive, "agent", "local.resource", &devService))
	if got.Decision != Approve || got.Snapshot != SnapshotMismatch {
		t.Fatalf("a tampered snapshot was used: %+v", got)
	}
}

func TestWorkingFilesMerge(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(s.Dir, "main.yaml"), []byte("version: 1\ntargets:\n  - match: {env: prod}\n    rules:\n      - {effect: [destructive], decision: deny}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteWorking(filepath.Join("providers", "kubernetes.yaml"), File{Version: FileVersion, Providers: map[string]Provider{"kubernetes": {Rules: []Rule{{Ops: []string{"delete_pod"}, Decision: Approve}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "applied.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, problems, err := s.LoadWorking()
	if err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
	if len(f.Targets) != 1 || len(f.Providers["kubernetes"].Rules) != 1 {
		t.Fatalf("merged %+v", f)
	}
}

func hasRule(r Result, id string) bool {
	for _, m := range r.Matched {
		if m.Rule == id {
			return true
		}
	}
	return false
}

// Posture is reserved in the snapshot for P2-5: validated, hashed, merged,
// and ignored by this evaluator.
func TestPostureIsReservedAndInert(t *testing.T) {
	f := File{Version: FileVersion, Posture: PosturePermissive,
		PostureRules: []PostureRule{{Match: TargetMatch{Env: "prod"}, Posture: PostureSecure}}}
	if problems := f.Validate(); len(problems) != 0 {
		t.Fatal(problems)
	}
	bad := File{Version: FileVersion, Posture: "open", PostureRules: []PostureRule{{Posture: "yolo"}}}
	if problems := bad.Validate(); len(problems) != 2 {
		t.Fatalf("problems %v", problems)
	}
	prod := target.ResourceLabels{ID: "p", Labels: target.Labels{Env: target.EnvProd}}
	r := req("local", "remove", contract.EffectDestructive, "agent", "local.resource", &prod)
	if p, rules := f.PostureFor(r); p != PostureSecure || len(rules) != 1 {
		t.Fatalf("posture %s %v", p, rules)
	}
	if a, b := BaselineOnly("x").Authorize(r), NewEvaluator(f, "x").Authorize(r); a.Decision != b.Decision {
		t.Fatalf("a posture changed a decision: %s vs %s", a.Decision, b.Decision)
	}
	one, _ := Encode(File{Version: FileVersion})
	two, _ := Encode(f)
	if Hash(one) == Hash(two) {
		t.Fatal("posture is not part of the snapshot hash")
	}
	if Merge(File{Posture: PosturePermissive}, File{PostureRules: f.PostureRules}).Posture != PosturePermissive {
		t.Fatal("merge dropped the posture")
	}
	// A snapshot with an unknown posture is refused like any invalid one.
	s := Store{Dir: t.TempDir()}
	if _, err := s.Apply(bad); err != nil {
		t.Fatal(err)
	}
	if _, st := s.Load(); !st.Mismatch() {
		t.Fatalf("an invalid posture loaded: %+v", st)
	}
}

// TargetMatch.Matches is the one target matcher, shared with P2-5's
// posture rules. Unknown is a value like any other, and adhoc matches only
// when the match asks.
func TestTargetMatcher(t *testing.T) {
	unlabeledResT := target.Resolve("ssh", "ssh.host", "", &unlabeledRes, false)
	adhoc := target.Resolve("docker", "docker.container", "x", nil, true)
	stage := target.Resolve("docker", "docker.container", "", &stageHost, false)
	yes, no := true, false
	for _, c := range []struct {
		m         TargetMatch
		connector string
		t         target.Target
		want      bool
	}{
		{TargetMatch{Env: "unknown"}, "ssh", unlabeledResT, true},
		{TargetMatch{Env: "!prod"}, "ssh", unlabeledResT, true},
		{TargetMatch{Env: "prod"}, "ssh", unlabeledResT, false},
		{TargetMatch{Admin: "unknown"}, "ssh", unlabeledResT, true},
		{TargetMatch{Owner: "!self"}, "docker", stage, true},
		{TargetMatch{Admin: "self", Connector: "docker"}, "docker", stage, true},
		{TargetMatch{Admin: "self", Connector: "ssh"}, "docker", stage, false},
		{TargetMatch{Tags: []string{"poc"}}, "docker", stage, false},
		{TargetMatch{Adhoc: &yes}, "docker", adhoc, true},
		{TargetMatch{Adhoc: &yes}, "docker", stage, false},
		{TargetMatch{Adhoc: &no}, "docker", adhoc, false},
		{TargetMatch{}, "docker", adhoc, true},
	} {
		if got := c.m.Matches(c.connector, c.t); got != c.want {
			t.Errorf("%+v on %s %+v = %t, want %t", c.m, c.connector, c.t, got, c.want)
		}
	}
}
