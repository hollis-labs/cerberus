package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// `cerberus policy` and `cerberus posture` against main's output. The goldens policy-*.txt|json
// and posture-*.txt|json were printed by main's binary (built from `git archive origin/main`)
// under a scratch HOME with no config, no daemon and no terminal, the report's over the chain
// policyFixture writes (as ~/.cerberus/audit); posture-set-permissive.txt on a terminal
// (script(1)) up to its prompt. What differs on purpose is named where it is substituted:
// approvals are the owner's on the console (decision 0.1), not a passkey's, so the report's
// readiness and channels say so; and two host-wide switches say what this build does.

// policyServer is the API over an admin lane, a gate, and a policy store in a scratch config
// dir, with the report's audit chain, served to the CLI's client as *as.
type policyServer struct {
	*brakeServer
	store policy.Store
}

func newPolicyServer(t *testing.T) *policyServer {
	t.Helper()
	dir := t.TempDir()
	store := policy.Store{Dir: filepath.Join(dir, "policy"), AuditDir: filepath.Join(dir, "audit")}
	policyFixture(t, store.AuditDir)
	s := &brakeServer{as: &libauth.Principal{}, w: gatetest.New(t, gatetest.Options{})}
	*s.as = cliKey
	root := filepath.Join(dir, "adopted")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	svc, err := admin.New(admin.Options{Gate: s.w.Gate, Paths: admin.Paths{AdoptRoot: root, ConfigDir: t.TempDir()}, HostVersion: "test", Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Plugins().Stop(context.Background()) })
	a, err := api.New(api.Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir(), Gate: s.w.Gate, Admin: svc,
		Policy: &api.PolicyOptions{Store: store, Definitions: policyDefinitions(svc)}})
	if err != nil {
		t.Fatal(err)
	}
	mux := a.Mux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(libauth.WithPrincipal(r.Context(), *s.as)))
	}))
	t.Cleanup(srv.Close)
	s.c = &apiClient{base: srv.URL + "/api", key: "test", keyFile: "/keys/cli.key", http: srv.Client()}
	return &policyServer{brakeServer: s, store: store}
}

func (s *policyServer) run(t *testing.T, terminal bool, stdin string, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "cerberus", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(policyCommands(func() (*apiClient, error) { return s.c, nil }, func() bool { return terminal })...)
	var out bytes.Buffer
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

// The host-wide rows this build changes (api.HostWide), as main printed them and as this build
// does, for the global posture secure and permissive.
var hostWideSecure = map[string]string{
	"  plugin install: review summary and a confirmation on a terminal\n": "  plugin install: the owner accepts the review on the console\n",
	"  mcp-http listen: loopback only\n":                                  "  MCP over HTTP: /mcp on the serving Cerberus, behind its auth guard\n",
}

func TestPolicyAndPostureReadsMatchMain(t *testing.T) {
	s := newPolicyServer(t)
	for args, name := range map[string]string{
		"policy explain local.deploy":                "policy-explain-local-deploy.txt",
		"policy explain local.deploy -o json":        "policy-explain-local-deploy.json",
		"policy explain ssh.exec --as human --adhoc": "policy-explain-ssh-exec.txt",
		"posture show -o json":                       "posture-show.json",
		"posture show":                               "posture-show.txt",
	} {
		out, err := s.run(t, false, "", strings.Fields(args)...)
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		want := golden(t, name)
		if strings.HasPrefix(name, "posture-show") {
			for old, now := range hostWideSecure {
				want = strings.Replace(want, old, now, 1)
			}
			want = strings.Replace(want, `"now": "review summary and a confirmation on a terminal"`, `"now": "the owner accepts the review on the console"`, 1)
			want = strings.Replace(want, `"now": "loopback only",
      "switch": "mcp-http listen"`, `"now": "/mcp on the serving Cerberus, behind its auth guard",
      "switch": "MCP over HTTP"`, 1)
		}
		if out != want {
			t.Errorf("%s:\n got:\n%s\nwant (%s, the host-wide rows as this build has them):\n%s", args, out, name, want)
		}
	}
}

// mainNeed is the readiness main printed with no daemon: a passkey that could not be asked.
var mainNeed = regexp.MustCompile(`NOT READY: out-of-band approval with a passkey: the daemon did not report its passkeys \(.*\)\)`)

// collapse is a text table's line with its padding collapsed, so a column whose cells differ
// (NEEDS) does not move the others.
func collapse(s string) string {
	return regexp.MustCompile(` {2,}`).ReplaceAllString(s, " | ")
}

func TestPolicyReportMatchesMain(t *testing.T) {
	s := newPolicyServer(t)
	const oob = "out-of-band approval by the owner on the console"
	for args, name := range map[string]string{"policy report": "policy-report.txt", "policy report --scope principal=agent": "policy-report-agent.txt"} {
		out, err := s.run(t, false, "", strings.Fields(args)...)
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		var want []string
		for _, line := range strings.SplitAfter(golden(t, name), "\n") {
			switch {
			case strings.HasPrefix(line, "! 3 would-block decision(s) need a channel that is not ready"):
				continue // every approval is the owner's on the console: none is not ready
			case strings.HasPrefix(line, "Channels: "):
				line = "Channels: out_of_band ready (the owner approves on the console); tty_confirm ready: the serving Cerberus holds pending approvals for the owner to decide on the console.\n"
			}
			want = append(want, collapse(mainNeed.ReplaceAllString(line, oob)))
		}
		if got := collapse(out); got != strings.Join(want, "") {
			t.Errorf("%s (padding collapsed):\n got:\n%s\nwant (main's, readiness and channels for the console):\n%s", args, got, strings.Join(want, ""))
		}
	}
	out, err := s.run(t, false, "", "policy", "report", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got, want api.PolicyReport
	if err = json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(golden(t, "policy-report.json")), &want); err != nil {
		t.Fatal(err)
	}
	want.Channels, want.NotReady = got.Channels, 0
	for i := range want.Groups {
		if want.Groups[i].Channel == "out_of_band" {
			want.Groups[i].Ready, want.Groups[i].Need = true, oob
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("json:\n got: %+v\nwant (main's, readiness and channels for the console): %+v", got, want)
	}
	if !got.Channels.OutOfBand || !got.Channels.Daemon || got.Channels.Keys != 0 {
		t.Errorf("channels: %+v", got.Channels)
	}
}

// posture set shows what main showed before its phrase, on a terminal, with the same snapshot
// hash. The CLI's key is then asked for the owner's approval (printed, exit 0, nothing applied);
// once the owner approves it on the console, --approval applies exactly that change, without
// the phrase, recorded as old's policy apply. Off a terminal and without --approval it is
// refused as old refused it; an MCP caller is refused 428 with both owner paths named.
func TestPostureSetMatchesMainAndIsTheOwners(t *testing.T) {
	s := newPolicyServer(t)
	want := golden(t, "posture-set-permissive.txt")
	for old, now := range map[string]string{
		"  plugin install: review summary and a confirmation on a terminal -> summary printed; --yes skips the confirmation\n": "  plugin install: the owner accepts the review on the console -> the same: the owner accepts the review on the console (decision 0.1)\n",
		"  mcp-http listen: loopback only -> --insecure-listen allowed, with a warning and an audit record\n":                  "  MCP over HTTP: /mcp on the serving Cerberus, behind its auth guard -> the same: the posture does not open it\n",
	} {
		want = strings.Replace(want, old, now, 1)
	}
	if _, err := s.run(t, false, "", "posture", "set", "permissive"); !errors.Is(err, errPostureNotInteractive) {
		t.Fatalf("off a terminal: %v", err)
	}
	out, err := s.run(t, true, "nope\n", "posture", "set", "permissive")
	if err == nil || err.Error() != "the confirmation did not match; nothing was changed" || out != want {
		t.Fatalf("the preview:\n got:\n%s\nwant (main's, two host-wide rows as this build has them):\n%s\n%v", out, want, err)
	}
	*s.as = libauth.Principal{Verified: true, ID: api.LocalTrustID, Kind: libauth.KindOperator, Scopes: []string{api.ScopeAdmin}}
	_, err = s.run(t, true, "posture c3fce88ed9dd\n", "posture", "set", "permissive")
	if err == nil || !strings.Contains(err.Error(), "428 approval_required") || !strings.Contains(err.Error(), "--approval <id>") {
		t.Fatalf("an MCP caller: %v", err)
	}
	*s.as = cliKey
	out, err = s.run(t, true, "posture c3fce88ed9dd\n", "posture", "set", "permissive")
	if err != nil || !strings.HasPrefix(out, want) || !strings.Contains(out, "then run this again with --approval apr_") {
		t.Fatalf("the key's set:\n%s\n%v", out, err)
	}
	if s.store.CurrentPosture().Global != policy.PostureSecure {
		t.Fatal("a pending approval changed the posture")
	}
	id := s.onlyApproval(t)
	s.ownerApproves(t, id)
	out, err = s.run(t, false, "", "posture", "set", "permissive", "--approval", id)
	if err != nil || !strings.Contains(out, "This will set the global posture to permissive. Approved by the owner as "+id+".\nApplied ") ||
		!strings.Contains(out, "c3fce88ed9dd") || !strings.HasSuffix(out, ". The posture is now: permissive\n") {
		t.Fatalf("the approved set:\n%s\n%v", out, err)
	}
	if s.store.CurrentPosture().Global != policy.PosturePermissive {
		t.Fatal("the approved set did not apply")
	}
	var applied []audit.Record
	for _, r := range s.w.Records(audit.KindOutcome) {
		if r.Connector == "policy" && r.Operation == "apply" {
			applied = append(applied, r)
		}
	}
	if len(applied) == 0 || applied[len(applied)-1].Effect != "admin" || applied[len(applied)-1].ApprovalID != id || applied[len(applied)-1].Decision != audit.DecisionAllowed {
		t.Fatalf("the apply's record: %+v", applied)
	}
	if out, err = s.run(t, false, "", "posture", "show"); err != nil || !strings.HasPrefix(out, "Posture: permissive\n") {
		t.Fatalf("after: %s %v", out, err)
	}
}

// policy apply: the working files' change, the owner's approval, and an approval for one
// snapshot is not one for another (the files changed after it was approved).
func TestPolicyApplyIsTheOwners(t *testing.T) {
	s := newPolicyServer(t)
	write := func(body string) {
		if err := os.MkdirAll(s.store.Dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.store.Dir, "rules.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("version: 1\nposture: permissive\n")
	out, err := s.run(t, true, "nope\n", "policy", "apply")
	if err == nil || !strings.Contains(out, "Applying ") || !strings.Contains(out, " over baseline.\n") || !strings.Contains(out, "sampled decision(s) change:") {
		t.Fatalf("the preview:\n%s\n%v", out, err)
	}
	phrase := regexp.MustCompile(`Type "(apply [0-9a-f]{12})" to apply: `).FindStringSubmatch(out)
	if phrase == nil {
		t.Fatalf("no phrase:\n%s", out)
	}
	if out, err = s.run(t, true, phrase[1]+"\n", "policy", "apply"); err != nil || !strings.Contains(out, "--approval apr_") {
		t.Fatalf("the key's apply:\n%s\n%v", out, err)
	}
	id := s.onlyApproval(t)
	s.ownerApproves(t, id)
	write("version: 1\nposture: permissive\nposture_rules:\n  - match:\n      env: dev\n    posture: secure\n")
	if out, err = s.run(t, false, "", "policy", "apply", "--approval", id); err == nil {
		t.Fatalf("an approval for another snapshot applied:\n%s", out)
	}
	if _, status := s.store.LoadVerified(); status.Snapshot != policy.SnapshotBaseline {
		t.Fatalf("a refused apply applied %s", status.Snapshot)
	}
}

// policy enforce by the owner on the console applies directly and records enforcement_changed.
func TestPolicyEnforceByTheOwnerOnTheConsole(t *testing.T) {
	s := newPolicyServer(t)
	*s.as = theOwner
	out, err := s.run(t, true, "enforce everything\n", "policy", "enforce", "--all")
	if err != nil || !strings.Contains(out, "Enforcement: shadow") || !strings.Contains(out, "This will enforce everything. Type \"enforce everything\" to apply: ") ||
		!strings.Contains(out, "Enforcement is now: ") {
		t.Fatalf("enforce --all:\n%s\n%v", out, err)
	}
	if len(s.w.Records(audit.KindEnforcementChanged)) != 1 {
		t.Fatalf("enforcement_changed: %+v", s.w.Records(audit.KindEnforcementChanged))
	}
	if _, err = s.run(t, true, "", "policy", "enforce"); err == nil || !strings.Contains(err.Error(), "name what to enforce") {
		t.Fatalf("enforce with no scope: %v", err)
	}
}
