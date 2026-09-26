package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

type fakeApprovals struct {
	a       approval.Approval
	decided []cerbapi.ApprovalDecisionArgs
	revoked int
}

func (f *fakeApprovals) GetApproval(context.Context, string) (approval.Approval, error) {
	return f.a, nil
}
func (f *fakeApprovals) DecideApproval(_ context.Context, _ string, args cerbapi.ApprovalDecisionArgs) (approval.Approval, error) {
	f.decided = append(f.decided, args)
	out := f.a
	out.Status = approval.Denied
	if args.Approve {
		out.Status = approval.Approved
	}
	return out, nil
}
func (f *fakeApprovals) RevokeApproval(context.Context, string, cerbapi.ApprovalRevokeArgs) (approval.Approval, error) {
	f.revoked++
	out := f.a
	out.Status = approval.Revoked
	return out, nil
}

func approvalsFixture(t *testing.T, terminal bool, channel string) *fakeApprovals {
	t.Helper()
	f := &fakeApprovals{a: approval.Approval{ID: "apr_1", Status: approval.Pending, Connector: "docker", Operation: "stop",
		Target: audit.Target{Kind: "docker.container", Resource: "web"}, Channel: channel, Scope: "once",
		Principal: audit.Principal{Kind: "agent", Via: "mcp_stdio", Client: "claude-code"}, PlanHash: "sha256:plan"}}
	oldTerm, oldClient, oldURL, oldBrowser := approvalsIsTerminal, newApprovalsClient, consoleApprovalURL, approvalsDecideFlags.browser
	approvalsIsTerminal = func() bool { return terminal }
	newApprovalsClient = func() (approvalsClient, error) { return f, nil }
	consoleApprovalURL = func(id string) (string, error) {
		return "http://localhost:4783/login?token=t&next=%2Fapprovals%3Fid%3D" + id, nil
	}
	approvalsDecideFlags.browser = false
	t.Cleanup(func() {
		approvalsIsTerminal, newApprovalsClient, consoleApprovalURL, approvalsDecideFlags.browser = oldTerm, oldClient, oldURL, oldBrowser
		approvalsDecideFlags.reason = ""
	})
	return f
}

func runApprovals(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetArgs(append([]string{"approvals"}, args...))
	rootCmd.SetOut(&out)
	rootCmd.SetIn(strings.NewReader(stdin))
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetIn(nil) })
	err := rootCmd.Execute()
	return out.String(), err
}

func TestApprovalDecisionsAreTerminalOnly(t *testing.T) {
	f := approvalsFixture(t, false, approval.ChannelTTYConfirm)
	for _, verb := range []string{"approve", "deny", "revoke"} {
		if _, err := runApprovals(t, "", verb, "apr_1"); !errors.Is(err, errApprovalsNotInteractive) {
			t.Fatalf("%s: %v", verb, err)
		}
	}
	if len(f.decided) != 0 || f.revoked != 0 || redact.Text(errApprovalsNotInteractive.Error()) != errApprovalsNotInteractive.Error() {
		t.Fatal("a refused decision reached the daemon, or its refusal did not survive redaction")
	}
}

// An approve shows the request in full, who asked and its plan included, and
// is confirmed by typing the target.
func TestApproveOnTheTerminalTypesTheTarget(t *testing.T) {
	f := approvalsFixture(t, true, approval.ChannelTTYConfirm)
	out, err := runApprovals(t, "yes\n", "approve", "apr_1")
	if err == nil || len(f.decided) != 0 {
		t.Fatalf("a wrong confirmation approved: %v", err)
	}
	for _, want := range []string{"agent via mcp_stdio (claude-code)", "sha256:plan", "Type the target (web) to approve"} {
		if !strings.Contains(out, want) {
			t.Errorf("approve does not show %q:\n%s", want, out)
		}
	}
	if _, err = runApprovals(t, "web\n", "approve", "apr_1", "--reason", "checked the plan"); err != nil || len(f.decided) != 1 || !f.decided[0].Approve || f.decided[0].Reason != "checked the plan" {
		t.Fatalf("approve: %v, %+v", err, f.decided)
	}
	if _, err = runApprovals(t, "", "deny", "apr_1"); err != nil || len(f.decided) != 2 || f.decided[1].Approve {
		t.Fatalf("deny: %v, %+v", err, f.decided)
	}
}

// An out-of-band approve is never decided on the terminal: it is sent to the
// console, where the passkey is.
func TestOutOfBandApproveGoesToTheConsole(t *testing.T) {
	f := approvalsFixture(t, true, approval.ChannelOutOfBand)
	out, err := runApprovals(t, "", "approve", "apr_1")
	if err != nil || len(f.decided) != 0 || !strings.Contains(out, "approved out of band, with a passkey, on the console") || !strings.Contains(out, "next=%2Fapprovals%3Fid%3Dapr_1") {
		t.Fatalf("out-of-band approve: %v, decided %+v\n%s", err, f.decided, out)
	}
	if _, err = runApprovals(t, "", "revoke", "apr_1"); err != nil || f.revoked != 1 {
		t.Fatalf("revoke: %v", err)
	}
}
