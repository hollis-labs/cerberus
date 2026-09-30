package cerbapi

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/connector"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
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

func withEgressPolicy(t *testing.T, rules ...policy.EgressRule) {
	t.Helper()
	withPDP(t, policy.NewEvaluator(policy.File{Version: policy.FileVersion, Egress: rules}, "egress-test"))
}

// Resource logs under egress policy: in shadow the agent gets every line and
// the outcome records what would have been withheld; enforced, the log is
// capped with a note; refused, the read answers egress_refused with guidance
// that survives redaction. A human, whom no rule names, gets it all.
func TestResourceLogsUnderEgressPolicy(t *testing.T) {
	sink := audit.NewMemory()
	svc := logsRuntime(t, sink)
	_, spec, err := svc.requireLocalProcessSpec("svc")
	if err != nil {
		t.Fatal(err)
	}
	res, _, _ := svc.requireLocalProcessSpec("svc")
	path := localconn.DevSessionLogPath(res.ID, spec)
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("one\ntwo\nthree\nfour\nfive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code"}
	lastOutcome := func() audit.Record { return outcome(sink.Records()) }

	withEgressPolicy(t, policy.EgressRule{ID: "cap-agent", Label: "untrusted", Action: policy.EgressCap, Lines: 2, Principal: &policy.PrincipalMatch{Kind: "agent"}})
	got, err := svc.ResourceLogs(as(agent), "svc", 10, "stdout")
	if err != nil || !strings.Contains(got.Content, "five") {
		t.Fatalf("shadow changed the log: %v %q", err, got.Content)
	}
	if e := lastOutcome().Egress; len(e) != 1 || e[0].Rule != "cap-agent" || e[0].Applied || e[0].Withheld != 3 || e[0].Pointers[0] != "/content" {
		t.Fatalf("shadow record: %+v", e)
	}

	withEgressPolicy(t, policy.EgressRule{ID: "cap-agent", Label: "untrusted", Action: policy.EgressCap, Lines: 2, Mode: policy.EgressEnforce, Principal: &policy.PrincipalMatch{Kind: "agent"}})
	got, err = svc.ResourceLogs(as(agent), "svc", 10, "stdout")
	if err != nil || got.Content != "one\ntwo\n[cerberus: 3 more lines withheld by egress rule cap-agent]" || got.ResourceID != "svc" {
		t.Fatalf("enforced cap: %v %+v", err, got)
	}
	if e := lastOutcome().Egress; len(e) != 1 || !e[0].Applied || e[0].Withheld != 3 {
		t.Fatalf("enforced record: %+v", e)
	}
	if got, err = svc.ResourceLogs(as(humanCLI), "svc", 10, "stdout"); err != nil || !strings.Contains(got.Content, "five") || len(lastOutcome().Egress) != 0 {
		t.Fatalf("a human: %v %+v", err, got)
	}

	withEgressPolicy(t, policy.EgressRule{ID: "no-logs", Label: "untrusted", Action: policy.EgressRefuse, Mode: policy.EgressEnforce, Principal: &policy.PrincipalMatch{Kind: "agent"}})
	_, err = svc.ResourceLogs(as(agent), "svc", 10, "stdout")
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Code != ExternalConnectorEgressRefused || !strings.Contains(err.Error(), "egress rule no-logs withholds its untrusted output") {
		t.Fatalf("refuse: %v", err)
	}
	if redact.Text(err.Error()) != err.Error() {
		t.Fatalf("the refusal did not survive redaction: %q", redact.Text(err.Error()))
	}
	if o := lastOutcome(); o.OutcomeCode != string(ExternalConnectorEgressRefused) || len(o.Egress) != 1 || !o.Egress[0].Applied {
		t.Fatalf("refusal record: %+v", o)
	}
}

// With no egress rules nothing is recorded or changed.
func TestNoEgressPolicyChangesNothing(t *testing.T) {
	c := &auditCall{spec: auditSpec{connector: "x", operation: "y"}}
	v := &LogLines{Content: "a"}
	if out, err := c.applyEgress(v); err != nil || out != any(v) || len(c.egress) != 0 {
		t.Fatalf("%v %v %v", out, err, c.egress)
	}
}

// An enforced refuse on an operation that is not a read (ssh exec) does not
// answer an error after the command ran: the call succeeds, its output is
// replaced by a note naming the rule, the command ran exactly once, and the
// outcome records refuse→withheld.
func TestEgressRefuseOnANonReadWithholdsAndSucceeds(t *testing.T) {
	backend := &fakeSSHBackend{}
	sink := audit.NewMemory()
	registry := connector.NewRegistry()
	registry.Register(sshconn.NewWithBackendFactory(nil, func() sshconn.Backend { return backend }))
	svc := NewExternalConnectorService(sink, registry)
	svc.SetResourceLookup(sshTestLookup())
	withEgressPolicy(t, policy.EgressRule{ID: "no-output", Label: "untrusted", Action: policy.EgressRefuse, Mode: policy.EgressEnforce})

	agent := Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code"}
	result, err := svc.Execute(as(agent), ExternalConnectorOperationArgs{Connector: "ssh", Operation: "exec", Acknowledged: true,
		Config: map[string]any{"id": "server-1", "command": "uptime"}})
	if err != nil {
		t.Fatalf("a refuse on a non-read failed the call: %v", err)
	}
	withheld, ok := result.Data.(map[string]any)
	note, _ := withheld["withheld"].(string)
	if !ok || !strings.Contains(note, "ssh exec ran and succeeded; its untrusted output is withheld by egress rule no-output") || withheld["egress_rule"] != "no-output" {
		t.Fatalf("result: %#v", result.Data)
	}
	if _, leaked := withheld["stdout"]; leaked || len(withheld) != 2 {
		t.Fatalf("the output leaked: %#v", result.Data)
	}
	if backend.execs != 1 {
		t.Fatalf("the command ran %d times", backend.execs)
	}
	if redact.Text(note) != note {
		t.Fatalf("the note did not survive redaction: %q", redact.Text(note))
	}
	o := outcome(sink.Records())
	if o.OutcomeCode != audit.OutcomeOK || len(o.Egress) != 1 || o.Egress[0].Action != EgressRefuseWithheld || !o.Egress[0].Applied {
		t.Fatalf("outcome: %+v", o)
	}
}
