package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/plan"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const confirmTestHash = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// scriptedExecutor asks for a confirmation until it gets one: a call is
// refused with a tty_confirm approval_pending, a plan request answers the
// plan, and a confirmed call runs.
type scriptedExecutor struct {
	calls []cerbapi.ExternalConnectorOperationArgs
}

func (s *scriptedExecutor) Execute(_ context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	s.calls = append(s.calls, args)
	switch {
	case args.Plan:
		return cerbapi.ExternalConnectorOperationResult{Data: map[string]any{"plan_hash": confirmTestHash, "computed_by": "socket",
			"plan": plan.Plan{Connector: "dnsdemo", Operation: "set_records", Effect: "write",
				Target: audit.Target{Kind: "dnsdemo.domain", Resource: "site", Env: "dev", Owner: "self", Admin: "self"}}}}, nil
	case args.ConfirmedPlanHash != "":
		return cerbapi.ExternalConnectorOperationResult{Data: map[string]any{"ok": true}}, nil
	}
	return cerbapi.ExternalConnectorOperationResult{}, &cerbapi.ExternalConnectorError{Code: cerbapi.ExternalConnectorApprovalPending,
		Connector: args.Connector, Operation: args.Operation, Approval: &cerbapi.ApprovalRef{ID: "apr_1", Channel: approval.ChannelTTYConfirm}}
}

func withTerminal(t *testing.T, tty bool) {
	t.Helper()
	was := confirmIsTerminal
	confirmIsTerminal = func() bool { return tty }
	t.Cleanup(func() { confirmIsTerminal = was })
}

func runConfirm(t *testing.T, typed string) (*scriptedExecutor, string, error) {
	t.Helper()
	exec := &scriptedExecutor{}
	var errOut bytes.Buffer
	err := runConnectorExec(context.Background(), &bytes.Buffer{}, &errOut, strings.NewReader(typed), exec, []contract.Definition{execTestDefinition()}, "dnsdemo", "set_records",
		connectorExecFlags{args: []string{"domain=example.com"}, ack: true})
	return exec, errOut.String(), err
}

// On a terminal, the plan is shown (effect, target and labels, where it was
// computed, the whole hash) and typing the target sends the call again,
// confirmed, naming the approval it decides.
func TestConnectorExecConfirmsOnTheTerminal(t *testing.T) {
	withTerminal(t, true)
	exec, shown, err := runConfirm(t, "site\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 3 || !exec.calls[1].Plan {
		t.Fatalf("calls %+v", exec.calls)
	}
	last := exec.calls[2]
	if last.ConfirmedPlanHash != confirmTestHash || last.ApprovalID != "apr_1" || last.Plan {
		t.Fatalf("confirmed call %+v", last)
	}
	for _, want := range []string{bold("Effect:      write"), bold("Computed by: socket"), "env dev, owner self, admin self", confirmTestHash, "Type the target (site)"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("the prompt does not show %q:\n%s", want, shown)
		}
	}
}

// "y" is not a confirmation, and neither is anything but the target:
// nothing is sent confirmed.
func TestConnectorExecRefusesAnythingButTheTarget(t *testing.T) {
	withTerminal(t, true)
	for _, typed := range []string{"y\n", "yes\n", "sit\n", ""} {
		exec, _, err := runConfirm(t, typed)
		if err == nil || !strings.Contains(err.Error(), "nothing ran") {
			t.Fatalf("%q: %v", typed, err)
		}
		if len(exec.calls) != 2 {
			t.Fatalf("%q: %d calls, want the refused call and the plan only", typed, len(exec.calls))
		}
	}
}

// Without a terminal the refusal is answered as it came, and no plan is
// asked for.
func TestConnectorExecWithoutATerminalDoesNotConfirm(t *testing.T) {
	withTerminal(t, false)
	exec, _, err := runConfirm(t, "site\n")
	if cerbapiCode(err) != cerbapi.ExternalConnectorApprovalPending || len(exec.calls) != 1 {
		t.Fatalf("err %v, %d calls", err, len(exec.calls))
	}
}

func cerbapiCode(err error) cerbapi.ExternalConnectorErrorCode {
	if coded, ok := err.(*cerbapi.ExternalConnectorError); ok { //nolint:errorlint // the scripted executor returns it unwrapped
		return coded.Code
	}
	return ""
}
