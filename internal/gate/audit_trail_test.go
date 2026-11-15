package gate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

func auditedDockerService(sink audit.Sink) *dockerService {
	return auditedDockerServiceWith(sink, &fakeDockerBackend{})
}

// pairs groups a sink's records into intent/outcome pairs by operation id.
func pairs(t *testing.T, recs []audit.Record) map[string][2]audit.Record {
	t.Helper()
	out := map[string][2]audit.Record{}
	for _, rec := range recs {
		p := out[rec.OperationID]
		switch rec.Kind {
		case audit.KindIntent:
			p[0] = rec
		case audit.KindOutcome:
			p[1] = rec
		}
		out[rec.OperationID] = p
	}
	for id, p := range out {
		if p[0].Kind != audit.KindIntent || p[1].Kind != audit.KindOutcome {
			t.Errorf("operation %s: unpaired records %+v", id, p)
		}
	}
	return out
}

// Every exit of the admin lane is recorded: a read that runs, a refusal by
// each gate, a dry run and a failure. Each writes an intent before anything
// happens and an outcome after, with the same operation id.
func TestAdminLaneRecordsEveryExit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     laneArgs
		decision string
		code     string
		effect   string
	}{
		{"read", laneArgs{Connector: "docker", Operation: "list_containers"}, audit.DecisionAllowed, audit.OutcomeOK, "read"},
		{"ack refusal", laneArgs{Connector: "docker", Operation: "destroy", Config: map[string]any{"container": "web"}}, audit.DecisionRefused, "acknowledgment_required", "destructive"},
		{"argument refusal", laneArgs{Connector: "docker", Operation: "logs", Config: map[string]any{"bogus": 1, "container": "web"}}, audit.DecisionRefused, "invalid_args", "read_sensitive"},
		{"undeclared", laneArgs{Connector: "docker", Operation: "wipe"}, audit.DecisionRefused, "operation_unsupported", ""},
		// What `connectors exec` now sends rather than refusing locally.
		{"unknown connector", laneArgs{Connector: "nope", Operation: "x"}, audit.DecisionRefused, "operation_unsupported", ""},
		{"dry run with no preview", laneArgs{Connector: "docker", Operation: "stop", DryRun: true, Acknowledged: true, Config: map[string]any{"container": "web"}}, audit.DecisionRefused, "preview_unsupported", "lifecycle"},
		{"acknowledged lifecycle", laneArgs{Connector: "docker", Operation: "start", Acknowledged: true, Config: map[string]any{"container": "web"}}, audit.DecisionAllowed, audit.OutcomeOK, "lifecycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := audit.NewMemory()
			_, _ = auditedDockerService(sink).Execute(WithCallerSurface(context.Background(), SurfaceSocket), tc.args)
			got := pairs(t, sink.Records())
			if len(got) != 1 {
				t.Fatalf("%d operations recorded, want 1", len(got))
			}
			for _, p := range got {
				intent, outcome := p[0], p[1]
				if intent.Seq > outcome.Seq {
					t.Fatal("outcome written before intent")
				}
				if outcome.Decision != tc.decision || outcome.OutcomeCode != tc.code || outcome.Effect != tc.effect {
					t.Fatalf("outcome %+v, want decision %s code %s effect %s", outcome, tc.decision, tc.code, tc.effect)
				}
				if intent.Principal.Surface != "socket" || !intent.Principal.SelfReported || intent.Posture != audit.PostureSecure {
					t.Fatalf("intent principal/posture: %+v", intent)
				}
				if intent.Acknowledged != tc.args.Acknowledged || intent.DryRun != tc.args.DryRun || !strings.HasPrefix(intent.ArgsDigest, "hmac-sha256:") {
					t.Fatalf("intent %+v", intent)
				}
			}
		})
	}
}

// The target is recorded from the contract's target fields; nothing else from
// the arguments is.
func TestAuditRecordsTheTargetAndNotTheArguments(t *testing.T) {
	sink := audit.NewMemory()
	_, _ = auditedDockerService(sink).Execute(context.Background(), laneArgs{
		Connector: "docker", Operation: "logs", Config: map[string]any{"container": "web", "lines": 5},
	})
	rec := sink.Records()[0]
	if rec.Target.Kind != "docker.container" || rec.Target.Fields["container"] != "web" {
		t.Fatalf("target %+v", rec.Target)
	}
	if _, ok := rec.Target.Fields["lines"]; ok {
		t.Fatal("a non-target argument was recorded")
	}
}

// Decision 8: when the log cannot be written, a non-read is refused before
// anything runs, with a coded error that survives redaction; a read goes on
// and is logged.
func TestUnwritableAuditRefusesNonReads(t *testing.T) {
	backend := &fakeDockerBackend{}
	svc := auditedDockerServiceWith(audit.Failing{}, backend)
	_, err := svc.Execute(context.Background(), laneArgs{Connector: "docker", Operation: "stop", Acknowledged: true, Config: map[string]any{"container": "web"}})
	var connErr *Error
	if !errors.As(err, &connErr) || connErr.Code != CodeAuditUnavailable {
		t.Fatalf("err = %v, want audit_unavailable", err)
	}
	if backend.stopped != "" {
		t.Fatal("a refused non-read ran")
	}
	if got := redact.Text(err.Error()); got != err.Error() || !strings.Contains(err.Error(), "nothing ran") {
		t.Fatalf("refusal lost its explanation: %q", got)
	}
	if status := HTTPStatus(err, 500); status != 503 {
		t.Fatalf("status %d, want 503", status)
	}

	if _, err := svc.Execute(context.Background(), laneArgs{Connector: "docker", Operation: "list_containers"}); err != nil || backend.lists != 1 {
		t.Fatalf("a read was refused because its record could not be written: %v", err)
	}
}

// A credential sent in config appears nowhere in any record: the arguments
// are recorded as a keyed digest, and only the target's fields by value.
func TestAuditRecordsCarryNoCredential(t *testing.T) {
	const configSentinel = "SENTINEL-CONFIG-7d3e"
	sink := audit.NewMemory()
	svc := auditedDockerService(sink)
	ctx := context.Background()
	for _, args := range []laneArgs{
		{Connector: "docker", Operation: "logs", Config: map[string]any{"container": "web", "api_token": configSentinel}},
		{Connector: "docker", Operation: "list_containers", Config: map[string]any{"password": configSentinel}},
		{Connector: "ssh", Operation: "exec", Config: map[string]any{"id": "box", "command": "export TOKEN=" + configSentinel}},
	} {
		_, _ = svc.Execute(ctx, args)
	}
	recs := sink.Records()
	if len(recs) < 6 {
		t.Fatalf("only %d records", len(recs))
	}
	data, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), configSentinel) {
		t.Fatalf("a record carries %q:\n%s", configSentinel, data)
	}
}

// The monitor's records (BeginAudit) are written and never authorized: an
// automation record carries no policy decision, and an automation call is
// never refused by enforcement.
func TestBeginAuditRecordsWithoutGating(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Deny})
	withEnforcement(t, nil)
	sink := audit.NewMemory()
	call, err := gateFor(sink).BeginAudit(context.Background(), Spec{Connector: "local", Operation: "apply", Automation: true, AutomationVia: "daemon_start"})
	if err != nil {
		t.Fatal(err)
	}
	call.Finish(nil)
	got := pairs(t, sink.Records())
	for _, p := range got {
		if p[0].Policy != nil || p[0].Principal.Kind != audit.PrincipalAutomation || p[0].Principal.Via != "daemon_start" || p[1].Decision != audit.DecisionAllowed {
			t.Fatalf("records %+v", p)
		}
	}
}
