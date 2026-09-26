package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	a, err := broker.Request(context.Background(), intent, policy.Result{Decision: policy.Approve}, channel, approval.ScopeOnce, time.Hour, "sha256:plan")
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
			got, err := broker.DecideAs(as(c.by), a.ID, ApprovalDecisionArgs{Approve: c.approve})
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
	if _, err := broker.DecideAs(as(humanWeb), a.ID, ApprovalDecisionArgs{Approve: true}); !errors.Is(err, errSelfApproval) {
		t.Fatalf("a console request approved on the console: %v", err)
	}
}

// Out of band, the labels are not enough: without a verified presence
// assertion an approve fails closed, whoever the decider says it is.
func TestOutOfBandApproveFailsClosedWithoutPresence(t *testing.T) {
	broker, a, _ := pendingApproval(t, agentMCP, approval.ChannelOutOfBand)
	_, err := broker.DecideAs(as(humanCLI), a.ID, ApprovalDecisionArgs{Approve: true})
	if !errors.Is(err, approval.ErrNoPresence) {
		t.Fatalf("an out-of-band approve without presence: %v", err)
	}
	if status, msg := approvalErrorStatus(err, a.ID); status != http.StatusForbidden || !strings.Contains(msg, "cerberus approvals enroll") || redact.Text(msg) != msg {
		t.Fatalf("status %d, message %q", status, msg)
	}
	if got, _ := broker.Get(a.ID); got.Status != approval.Pending {
		t.Fatalf("status = %s", got.Status)
	}
	if _, err := broker.DecideAs(as(humanCLI), a.ID, ApprovalDecisionArgs{Approve: false, Reason: "not now"}); err != nil {
		t.Fatalf("an out-of-band deny needs no presence: %v", err)
	}
}

// The socket's decide and revoke routes decide as the caller, and a refusal
// is a coded status with what to do.
func TestSocketDecideAndRevokeRoutes(t *testing.T) {
	broker, a, _ := pendingApproval(t, agentMCP, approval.ChannelTTYConfirm)
	SetBroker(broker)
	t.Cleanup(func() { SetBroker(nil) })
	s := &SocketServer{}
	post := func(p Principal, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(as(p))
		w, r := BeginHTTPRequest(rec, req, SurfaceSocket)
		s.handleApprovals(w, r.WithContext(WithPrincipal(r.Context(), p)))
		return rec
	}
	if rec := post(mcpCaller, "/approvals/"+a.ID+"/decide", `{"approve":true}`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "never approves") {
		t.Fatalf("MCP approve: %d %s", rec.Code, rec.Body.String())
	}
	rec := post(humanCLI, "/approvals/"+a.ID+"/decide", `{"approve":true,"reason":"looked at the plan"}`)
	var got approval.Approval
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Status != approval.Approved || got.Decision.By.Via != ViaCLI {
		t.Fatalf("CLI approve: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(humanCLI, "/approvals/"+a.ID+"/decide", `{"approve":false}`); rec.Code != http.StatusConflict {
		t.Fatalf("deciding twice: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(mcpCaller, "/approvals/"+a.ID+"/revoke", `{}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"revoked"`) {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(humanCLI, "/approvals/nope/decide", `{"approve":false}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
}
