package cerbapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
)

func logsRuntime(t *testing.T, sink audit.Sink) *ResourceRuntimeService {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: "svc", Type: "process", Connector: "local", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}, Config: map[string]any{
		"dir": t.TempDir(), "command": []string{"/bin/true"},
	}}}}
	return NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(cfg))
}

// Reading a resource's log is an operation like any other (I1): recorded as
// local.logs, read_sensitive, with an intent and an outcome
// (CERB-GAP-889).
func TestResourceLogsAreRecorded(t *testing.T) {
	sink := audit.NewMemory()
	svc := logsRuntime(t, sink)
	if _, err := svc.ResourceLogs(as(humanCLI), "svc", 5, "stdout"); err != nil {
		t.Fatal(err)
	}
	var intent, out audit.Record
	for _, r := range sink.Records() {
		switch r.Kind {
		case audit.KindIntent:
			intent = r
		case audit.KindOutcome:
			out = r
		}
	}
	if intent.Connector != "local" || intent.Operation != "logs" || intent.Effect != "read_sensitive" || out.OperationID != intent.OperationID {
		t.Fatalf("intent %+v, outcome %+v", intent, out)
	}
}

// Where policy is enforced, an agent reading a log needs an approval, and
// the read under it is spent once and is the requester's.
func TestResourceLogsNeedAnApprovalWhereEnforced(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	svc := logsRuntime(t, sink)
	agent := Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code"}

	_, err = svc.ResourceLogs(as(agent), "svc", 5, "stdout")
	var coded *ExternalConnectorError
	if connectorErrorCode(err) != ExternalConnectorApprovalPending || !errors.As(err, &coded) || coded.Approval == nil {
		t.Fatalf("an ungated log read: %v", err)
	}
	id := coded.Approval.ID
	if _, err = broker.DecideAs(as(humanCLI), id, ApprovalDecisionArgs{Approve: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ResourceLogs(as(Principal{Kind: PrincipalAgent, Via: ViaCLI, Client: "a-script"}), "svc", 5, "stdout", WithApprovalID(id)); connectorErrorCode(err) != ExternalConnectorApprovalRequired {
		t.Fatalf("another caller read under the approval: %v", err)
	}
	if _, err = svc.ResourceLogs(as(agent), "svc", 5, "stdout", WithApprovalID(id)); err != nil {
		t.Fatalf("the approved read: %v", err)
	}
	if got, _ := broker.Get(id); got.Status != approval.Consumed {
		t.Fatalf("status %s", got.Status)
	}
	if _, err = svc.ResourceLogs(as(agent), "svc", 5, "stdout", WithApprovalID(id)); connectorErrorCode(err) != ExternalConnectorApprovalRequired {
		t.Fatalf("second read under one approval: %v", err)
	}
}

// Over the socket, a log read's refusal keeps its code and approval (the
// route once turned every error into a plain 404), and approval_id travels
// with the read.
func TestResourceLogsApprovalOverTheSocket(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	client := startConnectorSocket(t, NewInProcessClient(WithResourceRuntimeService(logsRuntime(t, sink)), WithInProcessAudit(sink)))

	_, err = client.ResourceLogs(context.Background(), "svc", 5, "stdout")
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Code != ExternalConnectorApprovalPending || coded.Approval == nil {
		t.Fatalf("pending over the socket: %v", err)
	}
	if status, ok := DaemonHTTPStatus(err); !ok || status != http.StatusConflict {
		t.Fatalf("status %d %v", status, ok)
	}
	if _, err = broker.DecideAs(as(humanCLI), coded.Approval.ID, ApprovalDecisionArgs{Approve: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ResourceLogs(context.Background(), "svc", 5, "stdout", WithApprovalID(coded.Approval.ID)); err != nil {
		t.Fatalf("approved read over the socket: %v", err)
	}
	if _, err = client.ResourceLogs(context.Background(), "nope", 5, "stdout"); err == nil {
		t.Fatal("an unknown resource read")
	}
}
