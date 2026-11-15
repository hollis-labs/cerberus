package gate

import (
	"context"
	"errors"
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

func devStop() laneArgs {
	return laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"resource": "dev-box", "container": "web"}, Acknowledged: true}
}

// shownHash is the plan hash `connectors plan` shows for args.
func shownHash(ctx context.Context, t *testing.T, svc *dockerService, args laneArgs) string {
	t.Helper()
	args.Plan = true
	res, err := svc.Execute(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return res.(ConnectorPlan).PlanHash
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
	var coded *Error
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
		args      func(laneArgs, string, string) laneArgs
		code      ErrorCode
		says      string
	}{
		"another plan": {confirmHuman, func(a laneArgs, id, _ string) laneArgs {
			a.ApprovalID, a.ConfirmedPlanHash = id, "sha256:not-what-you-saw"
			return a
		}, CodePlanStale, "changed after you were shown it"},
		"an agent": {Principal{Kind: PrincipalAgent, Via: ViaCLI}, func(a laneArgs, _, hash string) laneArgs {
			a.ConfirmedPlanHash = hash
			return a
		}, CodeApprovalRequired, "a person at their own terminal"},
		"over MCP": {Principal{Kind: PrincipalHuman, Via: ViaMCPStdio}, func(a laneArgs, _, hash string) laneArgs {
			a.ConfirmedPlanHash = hash
			return a
		}, CodeApprovalRequired, "a person at their own terminal"},
		"web without a session": {Principal{Kind: PrincipalHuman, Via: ViaWeb}, func(a laneArgs, _, hash string) laneArgs {
			a.ConfirmedPlanHash = hash
			return a
		}, CodeApprovalRequired, "signed-in console session"},
		"someone else's approval": {Principal{Kind: PrincipalHuman, Via: ViaCLI, Session: "other"}, func(a laneArgs, id, hash string) laneArgs {
			a.ApprovalID, a.ConfirmedPlanHash = id, hash
			return a
		}, CodeApprovalRequired, "another caller"},
		"no such approval": {confirmHuman, func(a laneArgs, _, hash string) laneArgs {
			a.ApprovalID, a.ConfirmedPlanHash = "apr_000000000000", hash
			return a
		}, CodeApprovalRequired, "no approval"},
	} {
		t.Run(name, func(t *testing.T) {
			svc, backend := dockerLane(t, sink)
			asker := confirmHuman
			asker.Session = "mine"
			ctx := callerAs(asker, SurfaceSocket)
			_, err := svc.Execute(ctx, devStop())
			var coded *Error
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
	unlabeled := laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "web"}, Acknowledged: true}
	confirm := unlabeled
	confirm.ConfirmedPlanHash = shownHash(ctx, t, svc, unlabeled)
	_, err := svc.Execute(ctx, confirm)
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeApprovalPending || coded.Approval == nil || coded.Approval.Channel != approval.ChannelOutOfBand {
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
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeApprovalRequired || coded.Approval == nil || coded.Approval.Channel != approval.ChannelTTYConfirm {
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
	if _, err := svc.Execute(ctx, args); connectorErrorCode(err) != CodePlanStale || backend.stopped != "" {
		t.Fatalf("stale confirmation in shadow: %v", err)
	}
	args.ConfirmedPlanHash = shownHash(ctx, t, svc, devStop())
	if _, err := svc.Execute(ctx, args); err != nil || backend.stopped != "web" {
		t.Fatalf("confirmed in shadow: %v", err)
	}
}

// A signed-in console session confirms its own call like the CLI does.
func TestConsoleSessionConfirmsOnTheCall(t *testing.T) {
	sink := audit.NewMemory()
	broker := enforcedBroker(t, sink)
	svc, backend := dockerLane(t, sink)
	// As the console does: the request is marked web, then requireSession
	// puts the signed-in session's principal on it.
	ctx := WithPrincipal(BeginRequest(context.Background(), SurfaceWeb), WebSessionPrincipal("sess-1"))
	_, err := svc.Execute(ctx, devStop())
	var coded *Error
	if !errors.As(err, &coded) || coded.Approval == nil || coded.Approval.Channel != approval.ChannelTTYConfirm {
		t.Fatalf("asking: %v", err)
	}
	args := devStop()
	args.ApprovalID, args.ConfirmedPlanHash = coded.Approval.ID, shownHash(ctx, t, svc, devStop())
	if _, err := svc.Execute(ctx, args); err != nil || backend.stopped != "web" {
		t.Fatalf("confirmed from the console: %v", err)
	}
	if a, _ := broker.Get(coded.Approval.ID); a.Status != approval.Consumed || a.Decision.By.Via != ViaWeb || a.Decision.By.Session != "sess-1" {
		t.Fatalf("approval %+v", a)
	}
}

// An unlabeled target needs out of band, which only the daemon's broker
// can hold: a call made in-process with no broker, as the console's
// session, is asked for an approval, and a confirmation of the plan it was
// shown does not meet it. The plan itself is served and recorded as a dry
// run. (This used a deploy profile, the lane that moved to the vercel
// plugin; Decision 12 moves such gate tests onto a fake.)
func TestAConfirmationDoesNotMeetAnOutOfBandTarget(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	withEnforcement(t, nil)
	sink := audit.NewMemory()
	svc, backend := dockerLane(t, sink)
	ctx := WithPrincipal(BeginRequest(context.Background(), SurfaceWeb), WebSessionPrincipal("sess-1"))
	unlabeled := func() laneArgs {
		return laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "web"}, Acknowledged: true}
	}
	_, err := svc.Execute(ctx, unlabeled())
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeApprovalPending {
		t.Fatalf("unlabeled target: %v", err)
	}
	hash := shownHash(ctx, t, svc, unlabeled())
	if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("plan hash %q", hash)
	}
	if o := outcome(sink.Records()); !o.DryRun {
		t.Fatalf("a plan was not recorded as a dry run: %+v", o)
	}
	args := unlabeled()
	args.ConfirmedPlanHash = hash
	if _, err = svc.Execute(ctx, args); !errors.As(err, &coded) || coded.Code != CodeApprovalPending || backend.stopped != "" {
		t.Fatalf("a confirmation met an out-of-band approval: %v (stopped %q)", err, backend.stopped)
	}
}
