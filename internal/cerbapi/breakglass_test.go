package cerbapi

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// oobTwoApprovers asks for an out-of-band approval by two people: nothing a
// single person can meet except by breaking glass.
func oobTwoApprovers(t *testing.T) (*Broker, *audit.Memory) {
	t.Helper()
	withPDP(t, constantPDP{decision: policy.Approve, approval: &policy.Approval{Channel: approval.ChannelOutOfBand, Approvers: 2}})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	return broker, sink
}

type notes struct {
	mu   sync.Mutex
	sent []string
}

func (n *notes) install(t *testing.T) {
	SetNotifier(func(title, message string) { n.mu.Lock(); n.sent = append(n.sent, title+": "+message); n.mu.Unlock() })
	t.Cleanup(func() { SetNotifier(nil) })
}

func breakGlass(args ExternalConnectorOperationArgs, reason, typed string) ExternalConnectorOperationArgs {
	args.BreakGlass = &BreakGlassRequest{Reason: reason, Typed: typed}
	return args
}

var cliHuman = Principal{Kind: PrincipalHuman, Via: ViaCLI, Client: "cerberus-cli"}

// On a dev target a person breaks glass past an approve that asks for two
// out-of-band approvers: it runs, the break_glass record comes before the
// intent and links to it, the operator is notified, and a follow-up opens.
func TestBreakGlassGetsPastAnApprove(t *testing.T) {
	broker, sink := oobTwoApprovers(t)
	var n notes
	n.install(t)
	svc, backend := dockerLane(t, sink)
	ctx := callerAs(cliHuman, SurfaceSocket)
	if _, err := svc.Execute(ctx, breakGlass(devStop(), "the incident needs it now", "dev-box")); err != nil || backend.stopped != "web" {
		t.Fatalf("break glass: %v", err)
	}
	recs := sink.Records()
	var bgAt, intentAt = -1, -1
	for i, r := range recs {
		if r.Kind == audit.KindBreakGlass && bgAt < 0 {
			bgAt = i
		}
		if r.Kind == audit.KindIntent && intentAt < 0 {
			intentAt = i
		}
	}
	if bgAt < 0 || intentAt < 0 || bgAt > intentAt || recs[bgAt].OperationID != recs[intentAt].OperationID || recs[bgAt].BreakGlass.Reason != "the incident needs it now" {
		t.Fatalf("break_glass at %d, intent at %d: %+v", bgAt, intentAt, recs[bgAt])
	}
	open := broker.UnackedBreakGlass()
	if len(open) != 1 || open[0].Channel != approval.ChannelBreakGlass || open[0].Status != approval.Consumed {
		t.Fatalf("follow-ups %+v", open)
	}
	// A break-glass record shows what ran, like any approval (H3).
	if shown := open[0].Shown; shown == nil || len(shown.Plan) == 0 || !strings.Contains(string(shown.Arguments), `"web"`) {
		t.Fatalf("break glass shows %+v", open[0].Shown)
	}
	if o := outcome(recs); o.ApprovalID != open[0].ID {
		t.Fatalf("outcome %+v", o)
	}
	if len(n.sent) != 1 || !strings.Contains(n.sent[0], "break glass used") {
		t.Fatalf("notifications %v", n.sent)
	}
	if _, err := broker.AckBreakGlassAs(callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket), open[0].ID, "fine"); !errors.Is(err, errAckNotHuman) {
		t.Fatalf("an agent acknowledged a break glass: %v", err)
	}
	if _, err := broker.AckBreakGlassAs(ctx, open[0].ID, "reviewed"); err != nil || len(broker.UnackedBreakGlass()) != 0 {
		t.Fatalf("ack: %v", err)
	}
	if kinds := kinds(sink.Records(), "break_glass"); strings.Join(kinds, ",") != "break_glass,break_glass_acked" {
		t.Fatalf("break-glass records %v", kinds)
	}
}

// Break glass never gets past a deny, and asks for the target typed, a
// reason and a person at the CLI; nothing runs when it is refused.
func TestBreakGlassRefusals(t *testing.T) {
	_, sink := oobTwoApprovers(t)
	svc, backend := dockerLane(t, sink)
	for name, c := range map[string]struct {
		ctx  context.Context
		args ExternalConnectorOperationArgs
		says string
	}{
		"no reason":   {callerAs(cliHuman, SurfaceSocket), breakGlass(devStop(), "  ", "dev-box"), "needs a reason"},
		"wrong typed": {callerAs(cliHuman, SurfaceSocket), breakGlass(devStop(), "why", "dev"), "target typed exactly (dev-box)"},
		"an agent":    {callerAs(Principal{Kind: PrincipalAgent, Via: ViaCLI}, SurfaceSocket), breakGlass(devStop(), "why", "dev-box"), "a person at their own terminal"},
		"the console": {callerAs(WebSessionPrincipal("s"), SurfaceSocket), breakGlass(devStop(), "why", "dev-box"), "a person at their own terminal"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Execute(c.ctx, c.args)
			if connectorErrorCode(err) != ExternalConnectorApprovalRequired || !strings.Contains(err.Error(), c.says) || backend.stopped != "" {
				t.Fatalf("err = %v", err)
			}
		})
	}
	withPDP(t, constantPDP{decision: policy.Deny})
	if _, err := svc.Execute(callerAs(cliHuman, SurfaceSocket), breakGlass(devStop(), "why", "dev-box")); connectorErrorCode(err) != ExternalConnectorPolicyDenied || !strings.Contains(err.Error(), "never a deny") {
		t.Fatalf("a deny: %v", err)
	}
	withPDP(t, constantPDP{decision: policy.Approve})
	withEnforcement(t, nil)
	if _, err := svc.Execute(callerAs(cliHuman, SurfaceSocket), breakGlass(devStop(), "why", "dev-box")); connectorErrorCode(err) != ExternalConnectorApprovalRequired || !strings.Contains(err.Error(), "needs the daemon") {
		t.Fatalf("no daemon: %v", err)
	}
}

// Break glass on a target is limited per rolling window by the snapshot.
func TestBreakGlassIsRateLimited(t *testing.T) {
	_, sink := oobTwoApprovers(t)
	svc, backend := dockerLane(t, sink)
	ctx := callerAs(cliHuman, SurfaceSocket)
	for i := 0; i < policy.DefaultBreakGlassPerTarget; i++ {
		if _, err := svc.Execute(ctx, breakGlass(devStop(), "why", "dev-box")); err != nil {
			t.Fatalf("use %d: %v", i, err)
		}
	}
	backend.stopped = ""
	_, err := svc.Execute(ctx, breakGlass(devStop(), "why", "dev-box"))
	if connectorErrorCode(err) != ExternalConnectorApprovalRequired || !strings.Contains(err.Error(), "limited to 3 per target per 24h0m0s") || !strings.Contains(err.Error(), "next is possible at") || backend.stopped != "" {
		t.Fatalf("the fourth: %v", err)
	}
}

// On a protected target break glass is completed with a passkey on the
// console (D2): the call asks, a person approves with a passkey, and the
// retry naming the approval runs.
func TestBreakGlassOnAProtectedTargetNeedsAPasskey(t *testing.T) {
	broker, sink := oobTwoApprovers(t)
	broker.SetPresenceVerifier(acceptPresence{})
	svc, backend := dockerLane(t, sink)
	ctx := callerAs(cliHuman, SurfaceSocket)
	unlabeled := ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "web"}, Acknowledged: true}
	_, err := svc.Execute(ctx, breakGlass(unlabeled, "prod is down", "web"))
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Code != ExternalConnectorApprovalPending || !strings.Contains(err.Error(), "BREAK GLASS") || backend.stopped != "" {
		t.Fatalf("asking: %v", err)
	}
	a, _ := broker.Get(coded.Approval.ID)
	if a.BreakGlass == nil || a.BreakGlass.Reason != "prod is down" || a.Channel != approval.ChannelOutOfBand {
		t.Fatalf("pending break glass %+v", a)
	}
	if _, err := broker.DecideAs(callerAs(WebSessionPrincipal("s1"), SurfaceWeb), a.ID, ApprovalDecisionArgs{Approve: true}); err != nil {
		t.Fatalf("passkey approve: %v", err)
	}
	retry := breakGlass(unlabeled, "prod is down", "web")
	retry.ApprovalID = a.ID
	if _, err := svc.Execute(ctx, retry); err != nil || backend.stopped != "web" {
		t.Fatalf("retry: %v", err)
	}
	if open := broker.UnackedBreakGlass(); len(open) != 1 || open[0].ID != a.ID {
		t.Fatalf("follow-ups %+v", open)
	}
}

// Break glass travels only on its route, which refuses one with no reason;
// the operation's own route never reads it, and an older daemon's 404 says
// nothing ran.
func TestBreakGlassRoute(t *testing.T) {
	rec := &recordingClient{Client: NewInProcessClient()}
	client := startConnectorSocket(t, rec)
	if _, err := client.ExecuteConnectorOperation(context.Background(), breakGlass(devStop(), "why", "dev-box")); err != nil || rec.got.BreakGlass == nil || rec.got.BreakGlass.Typed != "dev-box" {
		t.Fatalf("break-glass route: %v %+v", err, rec.got.BreakGlass)
	}
	rec.got = ExternalConnectorOperationArgs{}
	body := map[string]any{"config": map[string]any{"container": "web"}, "break_glass": map[string]any{"reason": "why", "typed": "dev-box"}}
	_ = client.doJSON(context.Background(), "POST", "/connectors/docker/operations/stop", body, nil)
	if rec.got.Operation != "stop" || rec.got.BreakGlass != nil {
		t.Fatalf("the operation's own route read a break glass: %+v", rec.got)
	}
	err := client.doJSON(context.Background(), "POST", "/connectors/docker/operations/stop/break-glass", map[string]any{"break_glass": map[string]any{"typed": "dev-box"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs break_glass.reason") {
		t.Fatalf("no reason: %v", err)
	}
	old, ran := oldDaemon(t)
	if _, err := old.ExecuteConnectorOperation(context.Background(), breakGlass(devStop(), "why", "dev-box")); err == nil || !strings.Contains(err.Error(), "predates break glass") || ran.Load() != 0 {
		t.Fatalf("older daemon: %v", err)
	}
}
