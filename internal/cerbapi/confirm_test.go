package cerbapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

var confirmHuman = Principal{Kind: PrincipalHuman, Via: ViaCLI, Client: "cerberus-cli"}

func callerAs(p Principal, surface CallerSurface) context.Context {
	return BeginRequest(WithPrincipal(context.Background(), p), surface)
}

func devStop() ExternalConnectorOperationArgs {
	return ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"resource": "dev-box", "container": "web"}, Acknowledged: true}
}

// shownHash is the plan hash `connectors plan` shows for args.
func shownHash(ctx context.Context, t *testing.T, svc *ExternalConnectorService, args ExternalConnectorOperationArgs) string {
	t.Helper()
	args.Plan = true
	res, err := svc.Execute(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return res.Data.(ConnectorPlan).PlanHash
}

func enforcedBroker(t *testing.T, sink audit.Sink) *Broker {
	t.Helper()
	withPDP(t, constantPDP{decision: policy.Approve})
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	return broker
}

// A person at their own terminal confirms the plan they were shown: the
// pending approval the first attempt asked for is decided and consumed in
// the confirming call, which runs once and records both.
func TestConfirmOnTheCall(t *testing.T) {
	sink := audit.NewMemory()
	broker := enforcedBroker(t, sink)
	svc, backend := dockerLane(t, sink)
	ctx := callerAs(confirmHuman, SurfaceSocket)
	_, err := svc.Execute(ctx, devStop())
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil || coded.Approval.Channel != approval.ChannelTTYConfirm {
		t.Fatalf("asking: %v", err)
	}
	args := devStop()
	args.ApprovalID, args.ConfirmedPlanHash = coded.Approval.ID, shownHash(ctx, t, svc, devStop())
	if _, err := svc.Execute(ctx, args); err != nil {
		t.Fatalf("confirmed call: %v", err)
	}
	if backend.stopped != "web" {
		t.Fatal("the confirmed call did not run")
	}
	a, _ := broker.Get(coded.Approval.ID)
	if a.Status != approval.Consumed || a.Decision == nil || a.Decision.Surface != ConfirmSurface || a.Decision.By.Kind != "human" {
		t.Fatalf("approval %+v", a)
	}
	if len(broker.List()) != 1 {
		t.Fatalf("confirming left %d approvals, want the one it decided", len(broker.List()))
	}
	if o := outcome(sink.Records()); o.ApprovalID != a.ID || o.PlanHash != args.ConfirmedPlanHash {
		t.Fatalf("outcome %+v", o)
	}
}

// Every way a confirmation can be wrong refuses, runs nothing, and leaves a
// pending approval pending.
func TestConfirmRefusals(t *testing.T) {
	sink := audit.NewMemory()
	broker := enforcedBroker(t, sink)
	for name, c := range map[string]struct {
		principal Principal
		args      func(ExternalConnectorOperationArgs, string, string) ExternalConnectorOperationArgs
		code      ExternalConnectorErrorCode
		says      string
	}{
		"another plan": {confirmHuman, func(a ExternalConnectorOperationArgs, id, _ string) ExternalConnectorOperationArgs {
			a.ApprovalID, a.ConfirmedPlanHash = id, "sha256:not-what-you-saw"
			return a
		}, ExternalConnectorPlanStale, "changed after you were shown it"},
		"an agent": {Principal{Kind: PrincipalAgent, Via: ViaCLI}, func(a ExternalConnectorOperationArgs, _, hash string) ExternalConnectorOperationArgs {
			a.ConfirmedPlanHash = hash
			return a
		}, ExternalConnectorApprovalRequired, "a person at their own terminal"},
		"over MCP": {Principal{Kind: PrincipalHuman, Via: ViaMCPStdio}, func(a ExternalConnectorOperationArgs, _, hash string) ExternalConnectorOperationArgs {
			a.ConfirmedPlanHash = hash
			return a
		}, ExternalConnectorApprovalRequired, "a person at their own terminal"},
		"someone else's approval": {Principal{Kind: PrincipalHuman, Via: ViaCLI, Session: "other"}, func(a ExternalConnectorOperationArgs, id, hash string) ExternalConnectorOperationArgs {
			a.ApprovalID, a.ConfirmedPlanHash = id, hash
			return a
		}, ExternalConnectorApprovalRequired, "another caller"},
		"no such approval": {confirmHuman, func(a ExternalConnectorOperationArgs, _, hash string) ExternalConnectorOperationArgs {
			a.ApprovalID, a.ConfirmedPlanHash = "apr_000000000000", hash
			return a
		}, ExternalConnectorApprovalRequired, "no approval"},
	} {
		t.Run(name, func(t *testing.T) {
			svc, backend := dockerLane(t, sink)
			asker := confirmHuman
			asker.Session = "mine"
			ctx := callerAs(asker, SurfaceSocket)
			_, err := svc.Execute(ctx, devStop())
			var coded *ExternalConnectorError
			if !errors.As(err, &coded) || coded.Approval == nil {
				t.Fatalf("asking: %v", err)
			}
			p := c.principal
			if p.Session == "" && p.Kind == asker.Kind && p.Via == asker.Via {
				p.Session = asker.Session
			}
			_, err = svc.Execute(callerAs(p, SurfaceSocket), c.args(devStop(), coded.Approval.ID, shownHash(ctx, t, svc, devStop())))
			if connectorErrorCode(err) != c.code || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("err = %v", err)
			}
			if got := redact.Text(err.Error()); got != err.Error() {
				t.Fatalf("redaction changed the refusal:\n  %s\n  %s", err.Error(), got)
			}
			if backend.stopped != "" {
				t.Fatal("a refused confirmation ran")
			}
			if a, _ := broker.Get(coded.Approval.ID); a.Status != approval.Pending {
				t.Fatalf("a refused confirmation changed the approval: %s", a.Status)
			}
		})
	}
}

// A target that needs out of band cannot be confirmed on the call: the
// answer is approval_pending, as without a confirmation.
func TestConfirmIsNotEnoughOutOfBand(t *testing.T) {
	sink := audit.NewMemory()
	enforcedBroker(t, sink)
	svc, backend := dockerLane(t, sink)
	ctx := callerAs(confirmHuman, SurfaceSocket)
	unlabeled := ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "web"}, Acknowledged: true}
	confirm := unlabeled
	confirm.ConfirmedPlanHash = shownHash(ctx, t, svc, unlabeled)
	_, err := svc.Execute(ctx, confirm)
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Code != ExternalConnectorApprovalPending || coded.Approval == nil || coded.Approval.Channel != approval.ChannelOutOfBand {
		t.Fatalf("err = %v", err)
	}
	if backend.stopped != "" {
		t.Fatal("an out-of-band target ran on a confirmation")
	}
}

// With no daemon (D3), a confirmation is recorded rather than stored:
// requested, decided and consumed under an id of its own, on the call's
// records.
func TestConfirmInProcess(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	withEnforcement(t, nil)
	sink := audit.NewMemory()
	svc, backend := dockerLane(t, sink)
	ctx := callerAs(confirmHuman, SurfaceInProcess)
	_, err := svc.Execute(ctx, devStop())
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Code != ExternalConnectorApprovalRequired || coded.Approval == nil || coded.Approval.Channel != approval.ChannelTTYConfirm {
		t.Fatalf("asking without a daemon: %v", err)
	}
	args := devStop()
	args.ConfirmedPlanHash = shownHash(ctx, t, svc, devStop())
	if _, err := svc.Execute(ctx, args); err != nil || backend.stopped != "web" {
		t.Fatalf("confirmed in process: %v", err)
	}
	var kinds []string
	var id string
	for _, rec := range sink.Records() {
		if strings.HasPrefix(rec.Kind, "approval_") {
			kinds, id = append(kinds, rec.Kind), rec.Approval.ID
		}
	}
	if strings.Join(kinds, ",") != "approval_requested,approval_decided,approval_consumed" || !strings.HasPrefix(id, "apr_") {
		t.Fatalf("approval records %v", kinds)
	}
	if o := outcome(sink.Records()); o.ApprovalID != id || o.PlanHash != args.ConfirmedPlanHash {
		t.Fatalf("outcome %+v", o)
	}
}

// Shadow mode asks for nothing, but a confirmed plan is still checked:
// whoever confirmed it was shown it.
func TestConfirmedPlanIsCheckedInShadow(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	svc, backend := dockerLane(t, sink)
	ctx := callerAs(confirmHuman, SurfaceSocket)
	args := devStop()
	args.ConfirmedPlanHash = "sha256:not-what-you-saw"
	if _, err := svc.Execute(ctx, args); connectorErrorCode(err) != ExternalConnectorPlanStale || backend.stopped != "" {
		t.Fatalf("stale confirmation in shadow: %v", err)
	}
	args.ConfirmedPlanHash = shownHash(ctx, t, svc, devStop())
	if _, err := svc.Execute(ctx, args); err != nil || backend.stopped != "web" {
		t.Fatalf("confirmed in shadow: %v", err)
	}
}

// recordingClient is an in-process client that remembers what the socket
// handed it.
type recordingClient struct {
	Client
	got ExternalConnectorOperationArgs
}

func (r *recordingClient) ExecuteConnectorOperation(_ context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	r.got = args
	return ExternalConnectorOperationResult{Connector: args.Connector, Operation: args.Operation}, nil
}

// A confirmed plan hash is read only on the confirm route, which refuses a
// confirmation without one; on the operation's own route the field is
// ignored, so no body field can make a call look confirmed.
func TestConfirmRoute(t *testing.T) {
	rec := &recordingClient{Client: NewInProcessClient()}
	client := startConnectorSocket(t, rec)
	args := devStop()
	args.ConfirmedPlanHash = "sha256:abc"
	if _, err := client.ExecuteConnectorOperation(context.Background(), args); err != nil || rec.got.ConfirmedPlanHash != "sha256:abc" {
		t.Fatalf("confirm route: %v, got %q", err, rec.got.ConfirmedPlanHash)
	}
	rec.got = ExternalConnectorOperationArgs{}
	body := map[string]any{"config": map[string]any{"container": "web"}, "acknowledged": true, "confirmed_plan_hash": "sha256:abc"}
	_ = client.doJSON(context.Background(), http.MethodPost, "/connectors/docker/operations/stop", body, nil)
	if rec.got.Operation != "stop" || rec.got.ConfirmedPlanHash != "" {
		t.Fatalf("the operation's own route read a confirmed hash: %+v", rec.got)
	}
	body = map[string]any{"config": map[string]any{"container": "web"}, "acknowledged": true}
	err := client.doJSON(context.Background(), http.MethodPost, "/connectors/docker/operations/stop/confirm", body, nil)
	if err == nil || !strings.Contains(err.Error(), "needs confirmed_plan_hash") {
		t.Fatalf("confirm without a hash: %v", err)
	}
}

// A confirmation sent to a daemon that predates confirming is refused, and
// the call does not run unconfirmed.
func TestConfirmNeverRunsOnAnOlderDaemon(t *testing.T) {
	client, ran := oldDaemon(t)
	args := devStop()
	args.ConfirmedPlanHash = "sha256:abc"
	_, err := client.ExecuteConnectorOperation(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "predates confirming") || ran.Load() != 0 {
		t.Fatalf("err = %v, ran %d", err, ran.Load())
	}
	if got := redact.Text(err.Error()); got != err.Error() {
		t.Fatalf("redaction ate the recovery:\n  %s\n  %s", err.Error(), got)
	}
}
