package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/hollis-labs/cerberus/internal/admin/admintest"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// approveAll is a policy that asks for an approval of every call, enforced everywhere.
type approveAll struct{}

func (approveAll) GlobalPosture() string { return policy.PostureSecure }
func (approveAll) Authorize(policy.Request) policy.Result {
	return policy.Result{Decision: policy.Approve, WouldBlock: true, Snapshot: "test",
		Matched: []policy.Match{{Rule: "test.approve", Decision: policy.Approve, Reason: "the test asks"}}}
}

type enforceAll struct{}

func (enforceAll) Enforced(policy.Request) bool { return true }

// runCLI runs cmds under a bare root with args and stdin; stdout and stderr are kept apart.
func runCLI(t *testing.T, cmds []*cobra.Command, stdin string, args ...string) (string, string, error) {
	t.Helper()
	root := &cobra.Command{Use: "cerberus", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(cmds...)
	var out, errOut bytes.Buffer
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// secretsCmd is `secrets` against s, on a terminal or not, reading value as the pasted line.
func (s *brakeServer) secretsCmd(terminal bool, value string) []*cobra.Command {
	return []*cobra.Command{secretsCommands(func() (*apiClient, error) { return s.c, nil }, func() bool { return terminal },
		func(out io.Writer) (string, error) { return readRawValue(strings.NewReader(value), out) })}
}

// `cerberus secrets set` over HTTP with the CLI's key: refused off a terminal and for a
// malformed name before anything is read; a name no installed connector declares is stored
// under service "cerberus", account "<service>/<key>" (the bootstrap order), with old's output
// and hint; a second set warns that a value exists.
func TestSecretsSetAtTheCLI(t *testing.T) {
	s := newReviewServer(t, "")
	if _, _, err := runCLI(t, s.secretsCmd(false, "v\n"), "", "secrets", "set", "keeper/ksm_config"); !errors.Is(err, errSecretsNotInteractive) {
		t.Fatalf("off a terminal: %v", err)
	}
	for _, bad := range []string{"keeper", "Keeper/ksm_config", "keeper/-x"} {
		if _, _, err := runCLI(t, s.secretsCmd(true, "v\n"), "", "secrets", "set", bad); err == nil || !strings.Contains(err.Error(), "keeper/ksm_config") {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if len(s.w.Sink.Records()) != 0 {
		t.Fatal("a refused name reached the gate")
	}
	out, _, err := runCLI(t, s.secretsCmd(true, "ksm-value\r"), "", "secrets", "set", "keeper/ksm_config")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "already has a value") || !strings.Contains(out, "Paste the value for keeper/ksm_config (it is not shown), then press Enter: \r\n") ||
		!strings.Contains(out, "Stored keeper/ksm_config (9 characters) in the OS credential store.\n") ||
		!strings.Contains(out, "run `cerberus connectors plugin managed unload keeper`, then `cerberus connectors plugin managed load keeper`") || strings.Contains(out, "ksm-value") {
		t.Fatalf("output:\n%s", out)
	}
	if got, err := keyring.Get("cerberus", "keeper/ksm_config"); err != nil || got != "ksm-value" {
		t.Fatalf("keyring: %q %v", got, err)
	}
	out, _, err = runCLI(t, s.secretsCmd(true, "ksm-value-2\n"), "", "secrets", "set", "keeper/ksm_config")
	if err != nil || !strings.Contains(out, "keeper/ksm_config already has a value; pasting replaces it.\n") {
		t.Fatalf("second set: %v\n%s", err, out)
	}
	var recorded bool
	for _, r := range s.w.Records("outcome") {
		recorded = recorded || (r.Connector == "secrets" && r.Operation == "set" && r.OutcomeCode == "ok" && r.Target.Fields["name"] == "keeper/ksm_config")
	}
	if !recorded {
		t.Fatalf("no secrets.set outcome: %+v", s.w.Sink.Records())
	}
	// Ctrl-C cancels: nothing is sent.
	before := len(s.w.Sink.Records())
	if _, _, err = runCLI(t, s.secretsCmd(true, "abc\x03"), "", "secrets", "set", "keeper/other"); err == nil || !strings.Contains(err.Error(), "canceled; nothing was stored") {
		t.Fatalf("ctrl-C: %v", err)
	}
	if len(s.w.Sink.Records()) != before {
		t.Fatal("a canceled set reached the gate")
	}
}

// Where policy asks for an approval, the first set is refused with the owner's approval to
// decide on the console (exit non-zero, nothing stored), and the same command with --approval
// and the same value stores it once the owner approved.
func TestSecretsSetUnderAnApproval(t *testing.T) {
	s := newAdminServer(t, "", gatetest.Options{Policy: approveAll{}, Enforcement: enforceAll{}}, nil)
	_, _, err := runCLI(t, s.secretsCmd(true, "op-token\n"), "", "secrets", "set", "onepassword/service_account_token")
	id := s.onlyApproval(t)
	if err == nil || !strings.Contains(err.Error(), "409 approval_pending") || !strings.Contains(err.Error(), "Approve "+id+" on the console") {
		t.Fatalf("first set: %v", err)
	}
	if _, kerr := keyring.Get("cerberus", "onepassword/service_account_token"); kerr == nil {
		t.Fatal("a pending set stored the value")
	}
	s.ownerApproves(t, id)
	out, _, err := runCLI(t, s.secretsCmd(true, "op-token\n"), "", "secrets", "set", "onepassword/service_account_token", "--approval", id)
	if err != nil || !strings.Contains(out, "Stored onepassword/service_account_token (8 characters)") {
		t.Fatalf("approved set: %v\n%s", err, out)
	}
	if got, _ := keyring.Get("cerberus", "onepassword/service_account_token"); got != "op-token" {
		t.Fatalf("keyring = %q", got)
	}
}

func TestReadRawValueEdits(t *testing.T) {
	var out bytes.Buffer
	if v, err := readRawValue(strings.NewReader("abX\x7fc\n"), &out); err != nil || v != "abc" {
		t.Fatalf("%q %v", v, err)
	}
	if _, err := readRawValue(strings.NewReader("ab"), &out); err == nil {
		t.Fatal("EOF before Enter is an error")
	}
}

// fakeMutator answers L7/L8 with a fixed result and keeps the request.
type fakeMutator struct {
	got *supervise.Request
	res *supervise.OpResult
	err error
}

func (f *fakeMutator) Mutate(_ context.Context, req supervise.Request) (*supervise.OpResult, error) {
	*f.got = req
	return f.res, f.err
}

func (s *brakeServer) resourceCmds() []*cobra.Command {
	r := &cobra.Command{Use: "resource"}
	r.AddCommand(resourceMutationCommands(func() (*apiClient, error) { return s.c, nil })...)
	return []*cobra.Command{r}
}

// The resource verbs over L7/L8: the body carries --ack, --approval and deploy's override; a
// run prints its message (or old's fallback) and warnings on stderr; a failed run prints the
// captured output on stderr and exits non-zero with its error; -o json prints the OpResult; a
// refusal before the run is the server's status, code and message.
func TestResourceVerbsAtTheCLI(t *testing.T) {
	var got supervise.Request
	m := &fakeMutator{got: &got, res: &supervise.OpResult{Success: true, ServiceID: "web", Warnings: []string{"drift"}}}
	s := newAdminServer(t, "", gatetest.Options{}, m)

	out, errOut, err := runCLI(t, s.resourceCmds(), "", "resource", "apply", "web", "--ack")
	if err != nil || out != "Applied resource web\n" || errOut != "Warning: drift\n" || got.Verb != "apply" || got.ID != "web" || !got.Acknowledged {
		t.Fatalf("apply: %v %q %q %+v", err, out, errOut, got)
	}
	m.res = &supervise.OpResult{Success: true, Message: "Deployed web (build ok)"}
	out, _, err = runCLI(t, s.resourceCmds(), "", "resource", "deploy", "web", "--ack", "--no-install-after-build", "--approval", "apr_1")
	if err != nil || out != "Deployed web (build ok)\n" || got.InstallAfterBuildOverride == nil || *got.InstallAfterBuildOverride || got.ApprovalID != "apr_1" {
		t.Fatalf("deploy: %v %q %+v", err, out, got)
	}
	if _, _, err = runCLI(t, s.resourceCmds(), "", "resource", "deploy", "web", "--ack"); err != nil || got.InstallAfterBuildOverride != nil {
		t.Fatalf("deploy without an override: %v %+v", err, got)
	}
	if _, _, err = runCLI(t, s.resourceCmds(), "", "resource", "deploy", "web", "--ack", "--install-after-build", "--no-install-after-build"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("both install flags: %v", err)
	}
	m.res = &supervise.OpResult{Success: false, BuildOutput: "make: *** [build] Error 2", BuildLogPath: "/logs/build.log", Error: "build failed: exit status 2"}
	out, errOut, err = runCLI(t, s.resourceCmds(), "", "resource", "deploy", "web", "--ack")
	if err == nil || err.Error() != "build failed: exit status 2" || out != "" ||
		!strings.Contains(errOut, "--- build output ---\nmake: *** [build] Error 2\n--------------------\n") || !strings.Contains(errOut, "build log: /logs/build.log\n") {
		t.Fatalf("failed deploy: %v %q %q", err, out, errOut)
	}
	out, _, err = runCLI(t, s.resourceCmds(), "", "resource", "stop", "web", "--ack", "-o", "json")
	if err == nil || !strings.Contains(out, `"success": false`) || !strings.Contains(out, `"error": "build failed: exit status 2"`) {
		t.Fatalf("stop -o json: %v %s", err, out)
	}
	m.res, m.err = nil, &supervise.Refusal{Status: http.StatusConflict, Code: "acknowledgment_required", Message: `stop of "web" changes the workload, so it needs an acknowledgment: run it again with --ack ("acknowledged": true)`}
	if _, _, err = runCLI(t, s.resourceCmds(), "", "resource", "stop", "web"); err == nil || !strings.Contains(err.Error(), "409 acknowledgment_required") || !strings.Contains(err.Error(), "run it again with --ack") {
		t.Fatalf("no --ack: %v", err)
	}
	m.err = gate.NewError("local", "remove", gate.CodeLockdown, errors.New("Cerberus is in lockdown"))
	if _, _, err = runCLI(t, s.resourceCmds(), "", "resource", "remove", "web", "--ack"); err == nil || !strings.Contains(err.Error(), "423 lockdown") {
		t.Fatalf("lockdown: %v", err)
	}
	m.err = nil
	m.res = &supervise.OpResult{Success: true, Plan: &gate.ConnectorPlan{PlanHash: "sha256:abc"}}
	out, _, err = runCLI(t, s.resourceCmds(), "", "resource", "plan", "web", "deploy", "--ack")
	if err != nil || !got.Plan || !got.Acknowledged || !strings.Contains(out, `"plan_hash": "sha256:abc"`) {
		t.Fatalf("plan: %v %s %+v", err, out, got)
	}
	if _, _, err = runCLI(t, s.resourceCmds(), "", "resource", "plan", "web", "explode"); err == nil || !strings.Contains(err.Error(), "unknown verb") {
		t.Fatalf("plan of an unknown verb: %v", err)
	}
}

// A gate approval on a resource verb: the CLI fails with the approval to decide on the console
// and the --approval command; a still-pending approval says so.
func TestResourceVerbApprovalPending(t *testing.T) {
	var got supervise.Request
	m := &fakeMutator{got: &got}
	s := newAdminServer(t, "", gatetest.Options{}, m)
	pending := &gate.Error{Code: gate.CodeApprovalPending, Connector: "local", Operation: "apply", Err: errors.New("local apply needs approval"),
		Approval: &gate.ApprovalRef{ID: "apr_x", Channel: "out_of_band"}}
	m.err = pending
	_, _, err := runCLI(t, s.resourceCmds(), "", "resource", "apply", "web", "--ack")
	if err == nil || !strings.Contains(err.Error(), "409 approval_pending") || !strings.Contains(err.Error(), "Approve apr_x on the console, signed in as the owner, then run this again with --approval apr_x") {
		t.Fatalf("pending: %v", err)
	}
	_, _, err = runCLI(t, s.resourceCmds(), "", "resource", "apply", "web", "--ack", "--approval", "apr_x")
	if err == nil || !strings.Contains(err.Error(), "approval apr_x is not approved yet") {
		t.Fatalf("still pending: %v", err)
	}
}

// The managed plugin lifecycle and `connectors exec|plan` over the admin routes: load an
// installed plugin, run its operation with a typed --arg, plan it, list, then unload.
func TestConnectorsExecAndManagedPluginsAtTheCLI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "adopted")
	if _, err := admintest.Install(root, echoPlugin, admintest.Plugin{}); err != nil {
		t.Fatal(err)
	}
	s := newReviewServer(t, root)
	cmds := pluginCommands(func() (*apiClient, error) { return s.c, nil }, func() bool { return false })
	out, _, err := runCLI(t, cmds, "", "connectors", "plugin", "managed", "load", "echo")
	if err != nil || !strings.Contains(out, `"loaded": true`) {
		t.Fatalf("load: %v %s", err, out)
	}
	out, errOut, err := runCLI(t, cmds, "", "connectors", "exec", "echo", "echo", "--arg", "message=hello")
	if err != nil || !strings.Contains(out, `"connector": "echo"`) || !strings.Contains(out, "hello") || errOut != "" {
		t.Fatalf("exec: %v %s %s", err, out, errOut)
	}
	out, _, err = runCLI(t, cmds, "", "connectors", "plan", "echo", "echo", "--arg", "message=hello")
	if err != nil || !strings.Contains(out, `"plan_hash"`) {
		t.Fatalf("plan: %v %s", err, out)
	}
	_, errOut, err = runCLI(t, cmds, "", "connectors", "exec", "nope", "echo")
	if err == nil || !strings.Contains(errOut, `hint: nope echo: connector "nope" not found`) {
		t.Fatalf("an unknown connector: %v %s", err, errOut)
	}
	out, _, err = runCLI(t, cmds, "", "connectors", "plugin", "managed", "list")
	if err != nil || !strings.Contains(out, `"id": "echo"`) {
		t.Fatalf("list: %v %s", err, out)
	}
	out, _, err = runCLI(t, cmds, "", "connectors", "plugin", "managed", "unload", "echo")
	if err != nil || !strings.Contains(out, `"loaded": false`) {
		t.Fatalf("unload: %v %s", err, out)
	}
}

// register, registry list, registry health and deregister write and read
// <adopt_root>/registry.yaml in place, in old's format, with old's output.
func TestRegistryVerbsAtTheCLI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "adopted")
	apps := filepath.Join(t.TempDir(), "apps")
	if err := os.MkdirAll(apps, 0o700); err != nil {
		t.Fatal(err)
	}
	desc := filepath.Join(apps, "demo.cerberus.yaml")
	if err := os.WriteFile(desc, []byte("kind: cerberus-project/v1\nowner: demo\nnamespace: local\nproject:\n  id: demo\n  name: Demo\nresources:\n"+
		"  - id: demo-dev\n    type: process\n    project: demo\n    connector: local\n    config:\n      mode: dev_session\n      command: [\"/bin/sleep\", \"1\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmds := registryCommands(func() (*registry.Registry, error) { return registry.ForRoot(root), nil })
	out, _, err := runCLI(t, cmds, "", "registry", "list")
	if err != nil || out != "No project configs registered.\n" {
		t.Fatalf("empty list: %v %q", err, out)
	}
	out, _, err = runCLI(t, cmds, "", "register", desc)
	if err != nil || out != "registered: demo [cerberus-project/v1] -> "+desc+"\n\nregistration is declarative — nothing is built or running yet.\nbuild and activate each resource with:\n  cerberus resource deploy demo-dev --ack\n" {
		t.Fatalf("register: %v %q", err, out)
	}
	idx, err := os.ReadFile(filepath.Join(root, "registry.yaml")) //nolint:gosec // the test's own tree
	if err != nil || !strings.Contains(string(idx), "owner: demo") || !strings.Contains(string(idx), "path: "+desc) {
		t.Fatalf("registry.yaml: %v %s", err, idx)
	}
	if out, _, err = runCLI(t, cmds, "", "registry", "list", "-o", "json"); err != nil || !strings.Contains(out, `"owner": "demo"`) {
		t.Fatalf("list -o json: %v %s", err, out)
	}
	if out, _, err = runCLI(t, cmds, "", "registry", "health"); err != nil || !strings.Contains(out, "demo") {
		t.Fatalf("health: %v %s", err, out)
	}
	if err = os.Remove(desc); err != nil {
		t.Fatal(err)
	}
	if _, _, err = runCLI(t, cmds, "", "registry", "health"); err == nil || !strings.Contains(err.Error(), "1 registered config(s) unhealthy") {
		t.Fatalf("health with the file gone: %v", err)
	}
	if out, _, err = runCLI(t, cmds, "", "deregister", "demo"); err != nil || out != "deregistered: demo\n" {
		t.Fatalf("deregister: %v %q", err, out)
	}
	if out, _, err = runCLI(t, cmds, "", "register", filepath.Join(apps, "missing.yaml")); err == nil || !strings.HasPrefix(err.Error(), "register: ") {
		t.Fatalf("a missing file: %v %q", err, out)
	}
}
