package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

func runPosture(t *testing.T, stdin string, cmdArgs ...string) (string, error) {
	t.Helper()
	postureFlags.scope, postureFlags.output = nil, outputFormatText
	var out bytes.Buffer
	rootCmd.SetArgs(append([]string{"posture"}, cmdArgs...))
	rootCmd.SetOut(&out)
	rootCmd.SetIn(strings.NewReader(stdin))
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetIn(nil); postureFlags.scope = nil })
	err := rootCmd.Execute()
	return out.String(), err
}

// confirmPhrase is the phrase a posture change asks for: the hash of what it
// would apply.
func confirmPhrase(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, `Type "posture `)
	if i < 0 {
		t.Fatalf("no confirmation prompt:\n%s", out)
	}
	rest := out[i+len(`Type "`):]
	return rest[:strings.Index(rest, `"`)]
}

// The posture changes only on a terminal, and the refusal survives redaction.
func TestPostureSetIsTerminalOnly(t *testing.T) {
	policyFixture(t, false)
	for _, args := range [][]string{{"set", "permissive"}, {"reset"}} {
		_, err := runPosture(t, "", args...)
		if !errors.Is(err, errPostureNotInteractive) {
			t.Fatalf("%v: err = %v", args, err)
		}
		if got := redact.Text(err.Error()); got != err.Error() {
			t.Fatalf("redaction rewrote the refusal: %q", got)
		}
	}
}

// set shows the host-wide switches and the sampled decisions that change,
// applies only on the typed confirmation, and records the apply.
func TestPostureSetShowsTheChangeAndAppliesOnConfirmation(t *testing.T) {
	store, sink := policyFixture(t, true)

	out, err := runPosture(t, "yes\n", "set", "permissive")
	if err == nil || !strings.Contains(out, "Host-wide switches change (the global posture goes secure -> permissive)") ||
		!strings.Contains(out, "local.deploy") || !strings.Contains(out, "posture.permissive") {
		t.Fatalf("a wrong confirmation: %v\n%s", err, out)
	}
	if got := store.CurrentPosture(); got.Global != policy.PostureSecure {
		t.Fatal("a wrong confirmation changed the posture")
	}
	if _, statErr := os.Stat(filepath.Join(store.Dir, policy.PostureFileName)); !os.IsNotExist(statErr) {
		t.Fatal("a wrong confirmation wrote posture.yaml")
	}

	out, err = runPosture(t, confirmPhrase(t, out)+"\n", "set", "permissive")
	if err != nil || !strings.Contains(out, "The posture is now: permissive") {
		t.Fatalf("set: %v\n%s", err, out)
	}
	if got := store.CurrentPosture(); got.Global != policy.PosturePermissive {
		t.Fatalf("posture after set = %+v", got)
	}
	recs := sink.Records()
	if len(recs) != 2 || recs[0].Operation != "apply" || recs[0].Effect != "admin" || recs[1].OutcomeCode != audit.OutcomeOK {
		t.Fatalf("records %+v", recs)
	}

	// policy apply afterwards applies posture.yaml again rather than
	// reverting it.
	if out, err = runPolicy(t, "", "apply"); err != nil || !strings.Contains(out, "nothing to apply") {
		t.Fatalf("policy apply after posture set: %v\n%s", err, out)
	}

	out, err = runPosture(t, "", "set", "permissive")
	if err != nil || !strings.Contains(out, "already the applied posture") {
		t.Fatalf("a no-op set: %v\n%s", err, out)
	}
}

// A scoped rule changes only policy evaluation, so the host-wide switches
// stay; reset --scope removes it, and reset returns to the default.
func TestPostureScopedRulesAndReset(t *testing.T) {
	store, _ := policyFixture(t, true)
	scoped := []string{"set", "permissive", "--scope", "env=dev", "--scope", "owner=self"}
	out, _ := runPosture(t, "", scoped...)
	if !strings.Contains(out, "Host-wide switches: unchanged") {
		t.Fatalf("a scoped rule must not change a host-wide switch:\n%s", out)
	}
	if _, err := runPosture(t, confirmPhrase(t, out)+"\n", scoped...); err != nil {
		t.Fatal(err)
	}
	if got := store.CurrentPosture(); got.Global != policy.PostureSecure || len(got.Rules) != 1 || got.Rules[0].Match != "env=dev owner=self" {
		t.Fatalf("posture = %+v", got)
	}

	out, _ = runPosture(t, "", "reset", "--scope", "env=dev", "--scope", "owner=self")
	if _, err := runPosture(t, confirmPhrase(t, out)+"\n", "reset", "--scope", "env=dev", "--scope", "owner=self"); err != nil {
		t.Fatal(err)
	}
	if got := store.CurrentPosture(); len(got.Rules) != 0 {
		t.Fatalf("rule not removed: %+v", got)
	}
	if _, err := runPosture(t, "", "reset", "--scope", "env=lab"); err == nil || !strings.Contains(err.Error(), "no posture rule for env=lab") {
		t.Fatalf("removing a missing rule: %v", err)
	}
}

// posture set never carries in unreviewed policy: not while the working files
// differ from the applied snapshot, not while another file declares the
// posture, and not over a snapshot that fails its hash check.
func TestPostureSetRefusesToCarryUnreviewedPolicy(t *testing.T) {
	store, _ := policyFixture(t, true)
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(store.Dir, name), []byte(body), 0o600); err != nil { //nolint:gosec // the test's own temp dir
			t.Fatal(err)
		}
	}

	write("main.yaml", "version: 1\nproviders:\n  local:\n    rules:\n      - {id: no-remove, ops: [remove], decision: deny}\n")
	if _, err := runPosture(t, "", "set", "permissive"); err == nil || !strings.Contains(err.Error(), "not applied") {
		t.Fatalf("unapplied working files: %v", err)
	}

	write("main.yaml", "version: 1\nposture: permissive\n")
	if _, err := runPosture(t, "", "set", "secure"); err == nil || !strings.Contains(err.Error(), "also declared in main.yaml") {
		t.Fatalf("posture declared elsewhere: %v", err)
	}
	if err := os.Remove(filepath.Join(store.Dir, "main.yaml")); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Apply(policy.File{Version: policy.FileVersion}); err != nil {
		t.Fatal(err)
	}
	applied := filepath.Join(store.Dir, "applied.yaml")
	data, _ := os.ReadFile(applied) //nolint:gosec // the test's own temp dir
	write("applied.yaml", string(data)+"\n# edited\n")
	if _, err := runPosture(t, "", "set", "permissive"); err == nil || !strings.Contains(err.Error(), "fails its hash check") {
		t.Fatalf("a mismatched snapshot: %v", err)
	}
}

func TestPostureShow(t *testing.T) {
	store, _ := policyFixture(t, false)
	if _, err := store.Apply(policy.File{Version: policy.FileVersion, Posture: policy.PosturePermissive,
		PostureRules: []policy.PostureRule{{Match: policy.TargetMatch{Env: "prod"}, Posture: policy.PostureSecure}}}); err != nil {
		t.Fatal(err)
	}
	out, err := runPosture(t, "", "show")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Posture: permissive; secure for env=prod", "plugin install: summary printed; --yes skips the confirmation",
		"env=prod: secure", "Never relaxed: the audit log, credential redaction, the --ack gate, and the deny on a target labeled admin: owner"} {
		if !strings.Contains(out, want) {
			t.Errorf("show is missing %q:\n%s", want, out)
		}
	}
}

func TestParseScope(t *testing.T) {
	m, err := parseScope([]string{"env=dev", "owner=!platform", "tag=a", "tag=b"})
	if err != nil || m.Env != "dev" || m.Owner != "!platform" || strings.Join(m.Tags, ",") != "a,b" {
		t.Fatalf("parse = %+v, %v", m, err)
	}
	for _, bad := range [][]string{{"env"}, {"env="}, {"color=red"}, {"env=dev", "env=lab"}} {
		if _, err := parseScope(bad); err == nil {
			t.Errorf("%v parsed", bad)
		}
	}
}
