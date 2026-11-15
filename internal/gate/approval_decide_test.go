package gate

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// pendingApproval asks a fresh broker for an approval on behalf of
// requester, through channel.
func pendingApproval(t *testing.T, requester audit.Principal, channel string) (*Broker, approval.Approval, *audit.Memory) {
	t.Helper()
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	intent := audit.Record{Kind: audit.KindIntent, OperationID: audit.NewID(), Principal: requester, Connector: "docker", Operation: "stop",
		Effect: "lifecycle", Target: audit.Target{Kind: "docker.container", Resource: "web", Env: "dev", Owner: "self"}, ArgsDigest: "digest"}
	a, err := broker.Request(context.Background(), intent, policy.Result{Decision: policy.Approve}, channel, approval.ScopeOnce, time.Hour, PlanSnapshot{Hash: "sha256:plan"})
	if err != nil {
		t.Fatal(err)
	}
	return broker, a, sink
}

func as(p Principal) context.Context {
	return WithPrincipal(BeginRequest(context.Background(), SurfaceSocket), p)
}

var (
	agentMCP  = audit.Principal{Kind: "agent", Surface: "socket", Via: ViaMCPStdio, Client: "claude-code"}
	humanCLI  = Principal{Kind: PrincipalHuman, Via: ViaCLI, Client: "cerberus-cli", UID: 501}
	humanWeb  = Principal{Kind: PrincipalHuman, Via: ViaWeb, Session: "s1"}
	agentCLI  = Principal{Kind: PrincipalAgent, Via: ViaCLI, Client: "cerberus-cli"}
	mcpCaller = Principal{Kind: PrincipalAgent, Via: ViaMCPHTTP, Client: "some-agent"}
)

// No self-approval (I5): an approve comes from a human, on a surface other
// than the request's, never from MCP; a deny is always allowed.
func TestNoSelfApproval(t *testing.T) {
	for _, c := range []struct {
		name    string
		by      Principal
		approve bool
		want    error
	}{
		{"a human on the CLI approves an MCP request", humanCLI, true, nil},
		{"a human on the console approves an MCP request", humanWeb, true, nil},
		{"an MCP client never approves", mcpCaller, true, errMCPNeverApproves},
		{"an agent on the CLI does not approve", agentCLI, true, errApproverNotHuman},
		{"an unknown surface does not approve", Principal{Kind: PrincipalHuman, Via: ViaUnknown}, true, errUnknownSurface},
		{"an MCP client may deny", mcpCaller, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			broker, a, _ := pendingApproval(t, agentMCP, approval.ChannelTTYConfirm)
			got, err := broker.DecideAs(as(c.by), a.ID, ApprovalDecisionArgs{Approve: c.approve}, OwnerSession{})
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.want == nil && got.Status != map[bool]approval.Status{true: approval.Approved, false: approval.Denied}[c.approve] {
				t.Fatalf("status = %s", got.Status)
			}
			if c.want != nil && got.Status != approval.Pending {
				t.Fatalf("a refused decision changed the approval: %s", got.Status)
			}
			if err != nil && redact.Text(err.Error()) != err.Error() {
				t.Fatalf("redaction rewrote the refusal: %q", redact.Text(err.Error()))
			}
		})
	}

	// The requester's own surface cannot approve, even as a human.
	webRequester := audit.Principal{Kind: "human", Surface: "socket", Via: ViaWeb, Session: "s0"}
	broker, a, _ := pendingApproval(t, webRequester, approval.ChannelTTYConfirm)
	if _, err := broker.DecideAs(as(humanWeb), a.ID, ApprovalDecisionArgs{Approve: true}, OwnerSession{}); !errors.Is(err, errSelfApproval) {
		t.Fatalf("a console request approved on the console: %v", err)
	}
}

// Out of band, the labels are not enough: without the owner's console
// session an approve fails closed, whoever the decider says it is.
func TestOutOfBandApproveFailsClosedWithoutPresence(t *testing.T) {
	broker, a, _ := pendingApproval(t, agentMCP, approval.ChannelOutOfBand)
	_, err := broker.DecideAs(as(humanCLI), a.ID, ApprovalDecisionArgs{Approve: true}, OwnerSession{})
	if !errors.Is(err, approval.ErrNoPresence) {
		t.Fatalf("an out-of-band approve without presence: %v", err)
	}
	if status, msg := ApprovalErrorStatus(err, a.ID); status != http.StatusForbidden || !strings.Contains(msg, "signed in to the console") || redact.Text(msg) != msg {
		t.Fatalf("status %d, message %q", status, msg)
	}
	if got, _ := broker.Get(a.ID); got.Status != approval.Pending {
		t.Fatalf("status = %s", got.Status)
	}
	if _, err := broker.DecideAs(as(humanCLI), a.ID, ApprovalDecisionArgs{Approve: false, Reason: "not now"}, OwnerSession{}); err != nil {
		t.Fatalf("an out-of-band deny needs no presence: %v", err)
	}
}

// Deciding and revoking decide as the caller, and a refusal maps to a coded
// status with what to do (the old build's socket routes, without the
// socket).
func TestDecideAndRevokeAsTheCaller(t *testing.T) {
	broker, a, _ := pendingApproval(t, agentMCP, approval.ChannelTTYConfirm)
	if _, err := broker.DecideAs(as(mcpCaller), a.ID, ApprovalDecisionArgs{Approve: true}, OwnerSession{}); err == nil {
		t.Fatal("an MCP client approved")
	} else if status, msg := ApprovalErrorStatus(err, a.ID); status != http.StatusForbidden || !strings.Contains(msg, "never approves") {
		t.Fatalf("MCP approve: %d %s", status, msg)
	}
	got, err := broker.DecideAs(as(humanCLI), a.ID, ApprovalDecisionArgs{Approve: true, Reason: "looked at the plan"}, OwnerSession{})
	if err != nil || got.Status != approval.Approved || got.Decision.By.Via != ViaCLI {
		t.Fatalf("CLI approve: %+v %v", got, err)
	}
	if _, err := broker.DecideAs(as(humanCLI), a.ID, ApprovalDecisionArgs{Approve: false}, OwnerSession{}); err == nil {
		t.Fatal("decided twice")
	} else if status, _ := ApprovalErrorStatus(err, a.ID); status != http.StatusConflict {
		t.Fatalf("deciding twice: %d", status)
	}
	if got, err := broker.RevokeAs(as(mcpCaller), a.ID, ApprovalRevokeArgs{}); err != nil || got.Status != approval.Revoked {
		t.Fatalf("revoke: %+v %v", got, err)
	}
	if _, err := broker.DecideAs(as(humanCLI), "nope", ApprovalDecisionArgs{Approve: false}, OwnerSession{}); err == nil {
		t.Fatal("an unknown id was decided")
	} else if status, _ := ApprovalErrorStatus(err, "nope"); status != http.StatusNotFound {
		t.Fatalf("unknown id: %d", status)
	}
}
