package gate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// A cap keeps the first lines and says how many it held back; a mask says
// what it hid; a list is capped by entries. Nothing goes silently.
func TestShapingSaysWhatItWithheld(t *testing.T) {
	cap2 := policy.EgressDecision{Rule: "r1", Label: "untrusted", Action: policy.EgressCap, Lines: 2}
	out, n := shapeText("a\nb\nc\nd", cap2)
	if out != "a\nb\n[cerberus: 2 more lines withheld by egress rule r1]" || n != 2 {
		t.Errorf("cap: %q %d", out, n)
	}
	if out, kept := shapeText("a\nb", cap2); out != "a\nb" || kept != 0 {
		t.Errorf("under the cap: %q %d", out, kept)
	}
	mask := policy.EgressDecision{Rule: "r2", Label: "personal", Action: policy.EgressMask}
	if out, masked := shapeText("ada@example.test", mask); out != "[cerberus: 16 characters of personal text masked by egress rule r2]" || masked != 16 {
		t.Errorf("mask: %q %d", out, masked)
	}
	doc := map[string]any{"lines": []any{"1", "2", "3"}, "rows": []any{map[string]any{"email": "a@x"}, map[string]any{"email": "b@y"}}}
	shaped, n := shapeAll(doc, []string{"/lines/*"}, cap2)
	if lines := shaped.(map[string]any)["lines"].([]any); len(lines) != 3 || lines[2] != "[cerberus: 1 more entries withheld by egress rule r1]" || n != 1 {
		t.Errorf("list cap: %v %d", lines, n)
	}
	shaped, n = shapeAll(doc, []string{"/rows/*/email"}, mask)
	if rows := shaped.(map[string]any)["rows"].([]any); !strings.Contains(rows[1].(map[string]any)["email"].(string), "masked") || n != 6 {
		t.Errorf("per-element mask: %v %d", rows, n)
	}
}

// withEgressPolicy applies rules the way the daemon does: an applied
// snapshot read through a policy.Reloading, the type app.installPolicy
// installs. An *Evaluator installed directly passed these tests while the
// shipped daemon applied no egress at all (H1).
func withEgressPolicy(t *testing.T, rules ...policy.EgressRule) {
	t.Helper()
	store := policy.Store{Dir: t.TempDir()}
	if _, err := store.Apply(policy.File{Version: policy.FileVersion, Egress: rules}); err != nil {
		t.Fatal(err)
	}
	withPDP(t, policy.NewReloading(store, nil))
}

// logLines is a resource's log as a lane returns it: its content is
// untrusted (the old build's LogLines).
type logLines struct {
	ResourceID string `json:"resource_id,omitempty"`
	Content    string `json:"content" cerb:"untrusted"`
}

// opResult is a lifecycle result: build and install output are untrusted
// (the old build's OpResult, trimmed).
type opResult struct {
	Success       bool   `json:"success"`
	ServiceID     string `json:"service_id"`
	Message       string `json:"message,omitempty"`
	BuildOutput   string `json:"build_output,omitempty" cerb:"untrusted"`
	InstallOutput string `json:"install_output,omitempty" cerb:"untrusted"`
}

// execResult is a command's result: its output is untrusted, its exit code
// is not.
type execResult struct {
	Stdout   string `json:"stdout" cerb:"untrusted"`
	Stderr   string `json:"stderr" cerb:"untrusted"`
	ExitCode int    `json:"exit_code"`
}

var logsOp = contract.Operation{Name: "logs", Effect: contract.EffectReadSensitive, Target: contract.TargetDescriptor{Kind: "local.resource", From: []string{"id"}},
	Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone}

// resourceLogs is a logs lane in front of the gate: begin, read, shape as
// the typed result, finish.
func resourceLogs(ctx context.Context, sink audit.Sink, content string) (*logLines, error) {
	call, err := gateFor(sink).Begin(ctx, Spec{Connector: "local", Operation: "logs", Op: logsOp, Known: true, Config: map[string]any{"id": "svc"}})
	if err != nil {
		return nil, err
	}
	out, err := ShapeAs(call, &logLines{ResourceID: "svc", Content: content})
	call.Finish(err)
	return out, err
}

// Resource logs under egress policy: in shadow the agent gets every line and
// the outcome records what would have been withheld; enforced, the log is
// capped with a note; refused, the read answers egress_refused with guidance
// that survives redaction. A human, whom no rule names, gets it all.
func TestResourceLogsUnderEgressPolicy(t *testing.T) {
	sink := audit.NewMemory()
	const log = "one\ntwo\nthree\nfour\nfive" // as the logs lane reads it: no trailing newline
	agent := Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code"}
	lastOutcome := func() audit.Record { return outcome(sink.Records()) }

	withEgressPolicy(t, policy.EgressRule{ID: "cap-agent", Label: "untrusted", Action: policy.EgressCap, Lines: 2, Principal: &policy.PrincipalMatch{Kind: "agent"}})
	got, err := resourceLogs(as(agent), sink, log)
	if err != nil || !strings.Contains(got.Content, "five") {
		t.Fatalf("shadow changed the log: %v %+v", err, got)
	}
	if e := lastOutcome().Egress; len(e) != 1 || e[0].Rule != "cap-agent" || e[0].Applied || e[0].Withheld != 3 || e[0].Pointers[0] != "/content" {
		t.Fatalf("shadow record: %+v", e)
	}

	withEgressPolicy(t, policy.EgressRule{ID: "cap-agent", Label: "untrusted", Action: policy.EgressCap, Lines: 2, Mode: policy.EgressEnforce, Principal: &policy.PrincipalMatch{Kind: "agent"}})
	got, err = resourceLogs(as(agent), sink, log)
	if err != nil || got.Content != "one\ntwo\n[cerberus: 3 more lines withheld by egress rule cap-agent]" || got.ResourceID != "svc" {
		t.Fatalf("enforced cap: %v %+v", err, got)
	}
	if e := lastOutcome().Egress; len(e) != 1 || !e[0].Applied || e[0].Withheld != 3 {
		t.Fatalf("enforced record: %+v", e)
	}
	if got, err = resourceLogs(as(humanCLI), sink, log); err != nil || !strings.Contains(got.Content, "five") || len(lastOutcome().Egress) != 0 {
		t.Fatalf("a human: %v %+v", err, got)
	}

	withEgressPolicy(t, policy.EgressRule{ID: "no-logs", Label: "untrusted", Action: policy.EgressRefuse, Mode: policy.EgressEnforce, Principal: &policy.PrincipalMatch{Kind: "agent"}})
	_, err = resourceLogs(as(agent), sink, log)
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeEgressRefused || !strings.Contains(err.Error(), "egress rule no-logs withholds its untrusted output") {
		t.Fatalf("refuse: %v", err)
	}
	if redact.Text(err.Error()) != err.Error() {
		t.Fatalf("the refusal did not survive redaction: %q", redact.Text(err.Error()))
	}
	if o := lastOutcome(); o.OutcomeCode != string(CodeEgressRefused) || len(o.Egress) != 1 || !o.Egress[0].Applied {
		t.Fatalf("refusal record: %+v", o)
	}
}

// With no egress rules nothing is recorded or changed.
func TestNoEgressPolicyChangesNothing(t *testing.T) {
	c := &Call{g: gateFor(audit.NewMemory()), spec: Spec{Connector: "x", Operation: "y"}}
	v := &logLines{Content: "a"}
	if out, err := c.Shape(v); err != nil || out != any(v) || len(c.egress) != 0 {
		t.Fatalf("%v %v %v", out, err, c.egress)
	}
}

// An enforced refuse on an operation that is not a read (ssh exec in the
// old build) does not answer an error after the command ran: the call
// succeeds, its output is replaced by a note naming the rule, and the
// outcome records refuse→withheld.
func TestEgressRefuseOnANonReadWithholdsAndSucceeds(t *testing.T) {
	sink := audit.NewMemory()
	withEgressPolicy(t, policy.EgressRule{ID: "no-output", Label: "untrusted", Action: policy.EgressRefuse, Mode: policy.EgressEnforce})
	execOp := contract.Operation{Name: "exec", Effect: contract.EffectExec, RequiresAck: true, Target: contract.TargetDescriptor{Kind: "ssh.host", From: []string{"id"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone}
	agent := Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code"}
	call, err := gateFor(sink).Begin(as(agent), Spec{Connector: "ssh", Operation: "exec", Op: execOp, Known: true, Acknowledged: true,
		Config: map[string]any{"id": "server-1", "command": "uptime"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := call.Shape(&execResult{Stdout: "up 3 days", Stderr: "", ExitCode: 0})
	call.Finish(err)
	if err != nil {
		t.Fatalf("a refuse on a non-read failed the call: %v", err)
	}
	// A typed result keeps its type and its success: the labeled fields carry
	// the note, and the exit code of the command that ran stays.
	withheld, ok := result.(map[string]any)
	note, _ := withheld["stdout"].(string)
	if !ok || note != "[cerberus: output withheld by egress rule no-output; the operation ran and succeeded]" ||
		withheld["stderr"] != note || withheld["exit_code"] != float64(0) {
		t.Fatalf("result: %#v", result)
	}
	if redact.Text(note) != note {
		t.Fatalf("the note did not survive redaction: %q", redact.Text(note))
	}
	o := outcome(sink.Records())
	if o.OutcomeCode != audit.OutcomeOK || len(o.Egress) != 1 || o.Egress[0].Action != EgressRefuseWithheld || !o.Egress[0].Applied {
		t.Fatalf("outcome: %+v", o)
	}
}

func agentCall(effect contract.Effect, connector, operation string) *Call {
	return &Call{
		g:      gateFor(audit.NewMemory()),
		spec:   Spec{Connector: connector, Operation: operation, Known: true, Op: contract.Operation{Name: operation, Effect: effect}},
		intent: audit.Record{Principal: audit.Principal{Kind: "agent"}},
	}
}

// A lifecycle result's build and install output are untrusted: capped when
// a rule enforces, with the success and the rest of the result intact; a
// refusal there withholds them and keeps the success.
func TestLifecycleResultsAreShaped(t *testing.T) {
	withEgressPolicy(t, policy.EgressRule{ID: "cap-builds", Label: "untrusted", Action: policy.EgressCap, Lines: 1, Mode: policy.EgressEnforce})
	out := &opResult{Success: true, ServiceID: "svc", Message: "deployed", BuildOutput: "go build\nwarning: x\nwarning: y"}
	call := agentCall(contract.EffectLifecycle, "local", "deploy")
	got, err := ShapeAs(call, out)
	if err != nil || !got.Success || got.Message != "deployed" || got.BuildOutput != "go build\n[cerberus: 2 more lines withheld by egress rule cap-builds]" {
		t.Fatalf("capped: %v %+v", err, got)
	}
	if len(call.egress) != 1 || call.egress[0].Pointers[0] != "/build_output" && call.egress[0].Pointers[0] != "/install_output" {
		t.Fatalf("recorded: %+v", call.egress)
	}

	withEgressPolicy(t, policy.EgressRule{ID: "no-builds", Label: "untrusted", Action: policy.EgressRefuse, Mode: policy.EgressEnforce})
	call = agentCall(contract.EffectLifecycle, "local", "deploy")
	got, err = ShapeAs(call, out)
	if err != nil || !got.Success || !strings.Contains(got.BuildOutput, "withheld by egress rule no-builds; the operation ran and succeeded") {
		t.Fatalf("withheld: %v %+v", err, got)
	}
	if call.egress[0].Action != EgressRefuseWithheld || !call.egress[0].Applied {
		t.Fatalf("recorded: %+v", call.egress)
	}
}

// A shadow rule stricter than an enforced one does not displace it (M-13):
// with an enforced cap beside a shadow refuse, an agent's log is capped,
// and the refuse is recorded beside the cap as not applied. It used to
// return the shadow refuse, so nothing was applied at all.
func TestAStricterShadowEgressRuleDoesNotDisplaceAnEnforcedOne(t *testing.T) {
	sink := audit.NewMemory()
	agent := &policy.PrincipalMatch{Kind: "agent"}
	withEgressPolicy(t,
		policy.EgressRule{ID: "cap-agent", Label: "untrusted", Action: policy.EgressCap, Lines: 2, Mode: policy.EgressEnforce, Principal: agent},
		policy.EgressRule{ID: "refuse-trial", Label: "untrusted", Action: policy.EgressRefuse, Principal: agent})
	got, err := resourceLogs(as(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}), sink, "one\ntwo\nthree\nfour\nfive\n")
	if err != nil || strings.Contains(got.Content, "five") || !strings.Contains(got.Content, "withheld by egress rule cap-agent") {
		t.Fatalf("the enforced cap was not applied: %v %+v", err, got)
	}
	e := outcome(sink.Records()).Egress
	var capApplied, refuseShadow bool
	for _, a := range e {
		capApplied = capApplied || (a.Rule == "cap-agent" && a.Applied)
		refuseShadow = refuseShadow || (a.Rule == "refuse-trial" && a.Action == policy.EgressRefuse && !a.Applied)
	}
	if !capApplied || !refuseShadow {
		t.Fatalf("records: %+v", e)
	}
}
