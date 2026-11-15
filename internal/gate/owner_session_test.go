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
)

// An out-of-band approval decided by the owner signed in to the console is
// approved, carries the owner-session proof (a digest, never the session
// id), and the retry naming it runs: consume checks the recorded decision
// and asks for no fresh proof.
func TestOwnerSessionApprovesOutOfBandAndIsConsumable(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	svc, backend := dockerLane(t, sink)
	agent := as(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code/2", Session: "s1"})
	// No resource: an unlabeled target, which is protected, so out of band.
	args := laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "web"}, Acknowledged: true}
	_, err = svc.Execute(agent, args)
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeApprovalPending || coded.Approval == nil || coded.Approval.Channel != approval.ChannelOutOfBand {
		t.Fatalf("asking: %v", err)
	}
	a, err := broker.DecideAs(asOwner(), coded.Approval.ID, ApprovalDecisionArgs{Approve: true, Reason: "checked"}, ownerConsole)
	if err != nil || a.Status != approval.Approved {
		t.Fatalf("the owner's console approve: %+v %v", a, err)
	}
	if !validOwnerSessionProof(a.Decision.KeyFingerprint) || strings.Contains(a.Decision.KeyFingerprint, ownerConsole.SessionID) {
		t.Fatalf("proof %q", a.Decision.KeyFingerprint)
	}
	args.ApprovalID = a.ID
	if _, err := svc.Execute(agent, args); err != nil || backend.stopped != "web" {
		t.Fatalf("the approved call: %v", err)
	}
	if got, _ := broker.Get(a.ID); got.Status != approval.Consumed {
		t.Fatalf("status %s", got.Status)
	}
	var decided audit.Record
	for _, rec := range sink.Records() {
		if rec.Kind == audit.KindApprovalDecided {
			decided = rec
		}
	}
	if decided.Approval == nil || decided.Approval.KeyFingerprint != a.Decision.KeyFingerprint {
		t.Fatalf("the decided record does not carry the verification: %+v", decided.Approval)
	}
}

// Without the owner's console session an out-of-band approve is refused as
// carrying no verification, whoever the principal label says decided: a
// human at the CLI, a self-reported web claim, or a console caller the HTTP
// layer did not establish as the owner (no session id).
func TestOutOfBandApproveWithoutTheOwnerSessionIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		ctx   context.Context
		owner OwnerSession
	}{
		"human at the CLI":                           {as(humanCLI), OwnerSession{}},
		"a web label, not a console session":         {asOwner(), OwnerSession{}},
		"a console person with no session":           {asOwner(), OwnerSession{ConsolePerson: true}},
		"a session the HTTP layer did not vouch for": {asOwner(), OwnerSession{SessionID: "sess_owner"}},
		"the owner's session, but over the CLI":      {as(humanCLI), ownerConsole},
	} {
		t.Run(name, func(t *testing.T) {
			broker, a, _ := pendingApproval(t, agentMCP, approval.ChannelOutOfBand)
			_, err := broker.DecideAs(c.ctx, a.ID, ApprovalDecisionArgs{Approve: true}, c.owner)
			if !errors.Is(err, approval.ErrNoPresence) {
				t.Fatalf("err = %v", err)
			}
			if got, _ := broker.Get(a.ID); got.Status != approval.Pending {
				t.Fatalf("status %s", got.Status)
			}
		})
	}
}

// A key (token) caller or an agent never meets an out-of-band approval, even
// claiming the owner's session.
func TestKeyOrAgentDeciderIsRefused(t *testing.T) {
	keyCaller := Principal{Kind: PrincipalAgent, Via: ViaMCPHTTP, AuthMethod: AuthOAuth, Subject: "key:ci", Issuer: "cerberus"}
	for name, c := range map[string]struct {
		by   Principal
		want error
	}{
		"a key caller":                         {keyCaller, errMCPNeverApproves},
		"an agent over the web claiming owner": {Principal{Kind: PrincipalAgent, Via: ViaWeb, Session: "sess_owner"}, errApproverNotHuman},
		"an agent at the CLI":                  {agentCLI, errApproverNotHuman},
	} {
		t.Run(name, func(t *testing.T) {
			broker, a, _ := pendingApproval(t, agentMCP, approval.ChannelOutOfBand)
			_, err := broker.DecideAs(as(c.by), a.ID, ApprovalDecisionArgs{Approve: true}, ownerConsole)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if got, _ := broker.Get(a.ID); got.Status != approval.Pending {
				t.Fatalf("status %s", got.Status)
			}
		})
	}
}

// The owner may approve on the console a request the console made: the
// owner's session is the exception to "another surface", as the passkey was.
// A tty_confirm request from the console is not.
func TestTheOwnerApprovesTheConsolesOwnOutOfBandRequest(t *testing.T) {
	webRequester := audit.Principal{Kind: "human", Surface: "web", Via: ViaWeb, Session: "sess_owner"}
	broker, a, _ := pendingApproval(t, webRequester, approval.ChannelOutOfBand)
	got, err := broker.DecideAs(asOwner(), a.ID, ApprovalDecisionArgs{Approve: true}, ownerConsole)
	if err != nil || got.Status != approval.Approved || !got.Decision.SameSurface {
		t.Fatalf("same-surface owner approve: %+v %v", got, err)
	}
	broker, a, _ = pendingApproval(t, webRequester, approval.ChannelTTYConfirm)
	if _, err := broker.DecideAs(asOwner(), a.ID, ApprovalDecisionArgs{Approve: true}, ownerConsole); !errors.Is(err, errSelfApproval) {
		t.Fatalf("a console tty_confirm approved on the console: %v", err)
	}
}

// The verifier accepts exactly the proofs DecideAs makes, on a human's
// approve over the web; a passkey-era decision, or a marker on another
// surface, is refused when the approval is used.
func TestOwnerSessionVerifierAcceptsOnlyItsProof(t *testing.T) {
	proof := ownerConsole.proof()
	web := audit.Principal{Kind: "human", Via: ViaWeb}
	for name, c := range map[string]struct {
		d  approval.Decision
		ok bool
	}{
		"the owner's proof":     {approval.Decision{Approve: true, By: web, KeyFingerprint: proof}, true},
		"a deny":                {approval.Decision{Approve: false, By: web, KeyFingerprint: proof}, false},
		"no proof":              {approval.Decision{Approve: true, By: web}, false},
		"a passkey fingerprint": {approval.Decision{Approve: true, By: web, KeyFingerprint: "sha256:abcdef", Assertion: []byte(`{"v":2}`)}, false},
		"the bare marker":       {approval.Decision{Approve: true, By: web, KeyFingerprint: ownerSessionPrefix}, false},
		"a raw session id":      {approval.Decision{Approve: true, By: web, KeyFingerprint: ownerSessionPrefix + "sess_owner"}, false},
		"over the CLI":          {approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Via: ViaCLI}, KeyFingerprint: proof}, false},
		"an agent":              {approval.Decision{Approve: true, By: audit.Principal{Kind: "agent", Via: ViaWeb}, KeyFingerprint: proof}, false},
	} {
		err := OwnerSessionVerifier{}.Verify(approval.Approval{Channel: approval.ChannelOutOfBand}, c.d)
		if (err == nil) != c.ok || (err != nil && !errors.Is(err, approval.ErrNoPresence)) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
