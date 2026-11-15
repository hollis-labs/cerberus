package gate

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// The verifier accepts exactly the proofs DecideAs makes, sealed with the key, on a human's
// approve over the web; a passkey-era decision, a marker on another surface, a seal made with
// another key or over another approval, an unsealed marker and a stale seal are refused.
func TestOwnerSessionVerifierAcceptsOnlyItsProof(t *testing.T) {
	proof := ownerConsole.proof()
	web := audit.Principal{Kind: "human", Via: ViaWeb}
	key, otherKey := audit.NewMemory(), audit.NewMemory()
	v := OwnerSessionVerifier{Key: key}
	a := approval.Approval{ID: "ap-1", Channel: approval.ChannelOutOfBand, ArgsDigest: "hmac-sha256:aa", PlanHash: "sha256:bb"}
	other := a
	other.ID = "ap-2"
	now := time.Now()
	sealed := v.Seal(a, proof, now)
	for name, c := range map[string]struct {
		a  approval.Approval
		d  approval.Decision
		v  OwnerSessionVerifier
		ok bool
	}{
		"the owner's sealed proof":     {a, approval.Decision{Approve: true, By: web, KeyFingerprint: proof, Assertion: sealed}, v, true},
		"recorded at the seal's time":  {a, approval.Decision{Approve: true, By: web, KeyFingerprint: proof, Assertion: sealed, At: now.Add(time.Second)}, v, true},
		"a deny":                       {a, approval.Decision{Approve: false, By: web, KeyFingerprint: proof, Assertion: sealed}, v, false},
		"no proof":                     {a, approval.Decision{Approve: true, By: web}, v, false},
		"the marker without a seal":    {a, approval.Decision{Approve: true, By: web, KeyFingerprint: proof}, v, false},
		"a seal made with another key": {a, approval.Decision{Approve: true, By: web, KeyFingerprint: proof, Assertion: OwnerSessionVerifier{Key: otherKey}.Seal(a, proof, now)}, v, false},
		"a seal for another approval":  {a, approval.Decision{Approve: true, By: web, KeyFingerprint: proof, Assertion: v.Seal(other, proof, now)}, v, false},
		"a seal for another session":   {a, approval.Decision{Approve: true, By: web, KeyFingerprint: proof, Assertion: v.Seal(a, OwnerSession{ConsolePerson: true, SessionID: "other"}.proof(), now)}, v, false},
		"recorded long after the seal": {a, approval.Decision{Approve: true, By: web, KeyFingerprint: proof, Assertion: sealed, At: now.Add(time.Hour)}, v, false},
		"no key":                       {a, approval.Decision{Approve: true, By: web, KeyFingerprint: proof, Assertion: sealed}, OwnerSessionVerifier{}, false},
		"a passkey fingerprint":        {a, approval.Decision{Approve: true, By: web, KeyFingerprint: "sha256:abcdef", Assertion: []byte(`{"v":2}`)}, v, false},
		"the bare marker":              {a, approval.Decision{Approve: true, By: web, KeyFingerprint: ownerSessionPrefix, Assertion: sealed}, v, false},
		"a raw session id":             {a, approval.Decision{Approve: true, By: web, KeyFingerprint: ownerSessionPrefix + "sess_owner", Assertion: sealed}, v, false},
		"over the CLI":                 {a, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Via: ViaCLI}, KeyFingerprint: proof, Assertion: sealed}, v, false},
		"an agent":                     {a, approval.Decision{Approve: true, By: audit.Principal{Kind: "agent", Via: ViaWeb}, KeyFingerprint: proof, Assertion: sealed}, v, false},
	} {
		err := c.v.Verify(c.a, c.d)
		if (err == nil) != c.ok || (err != nil && !errors.Is(err, approval.ErrNoPresence)) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A forged decision appended to the approvals store: a process that can write the store (here
// through the store's own API, so the store's hash chain is intact) but cannot read the digest
// key marks a pending approval approved with the owner-session marker. The approval cannot be
// used, and VerifyApprovals fails on it; the genuine owner-session approve beside it verifies,
// and a pre-cutover decision is listed as legacy, not failed.
func TestAForgedOwnerSessionDecisionFailsVerification(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	dir := filepath.Join(t.TempDir(), "approvals")
	broker, err := NewBroker(sink, dir)
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	svc, _ := dockerLane(t, sink)
	agent := as(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code/2", Session: "s1"})
	ask := func(container string) string {
		t.Helper()
		_, askErr := svc.Execute(agent, laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": container}, Acknowledged: true})
		var coded *Error
		if !errors.As(askErr, &coded) || coded.Approval == nil {
			t.Fatalf("asking: %v", askErr)
		}
		return coded.Approval.ID
	}
	genuine, forged, legacy := ask("web"), ask("db"), ask("cache")
	if _, err = broker.DecideAs(asOwner(), genuine, ApprovalDecisionArgs{Approve: true}, ownerConsole); err != nil {
		t.Fatal(err)
	}

	store, err := approval.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var trustAll trustingVerifier
	web := audit.Principal{Kind: "human", Via: ViaWeb}
	forgedSeal, _ := json.Marshal(map[string]any{"v": 1, "t": time.Now().UnixNano(), "mac": "hmac-sha256:" + strings.Repeat("0", 64)})
	if _, err = store.Decide(forged, approval.Decision{Approve: true, By: web, Surface: ViaWeb, KeyFingerprint: ownerConsole.proof(), Assertion: forgedSeal}, trustAll); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Decide(legacy, approval.Decision{Approve: true, By: web, Surface: ViaWeb, KeyFingerprint: "sha256:passkey-fingerprint", Assertion: []byte(`{"v":2}`)}, trustAll); err != nil {
		t.Fatal(err)
	}

	// The retry naming the forged approval, through a broker reopened over the edited store,
	// does not run.
	reopened, err := NewBroker(sink, dir)
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, reopened)
	svc2, backend := dockerLane(t, sink)
	_, err = svc2.Execute(agent, laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "db"}, Acknowledged: true, ApprovalID: forged})
	if err == nil || backend.stopped == "db" {
		t.Fatalf("the forged approval was usable: %v (stopped %q)", err, backend.stopped)
	}
	if !errors.Is(err, approval.ErrNoPresence) && !strings.Contains(err.Error(), "presence") && !strings.Contains(err.Error(), "verif") {
		t.Fatalf("refused for another reason than its proof: %v", err)
	}
	report, err := VerifyApprovals(dir, sink)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(report.Verified, ",") != genuine || strings.Join(report.Failed, ",") != forged || strings.Join(report.Legacy, ",") != legacy {
		t.Fatalf("report %+v, want verified %s, failed %s, legacy %s", report, genuine, forged, legacy)
	}
	if report.Err() == nil || !strings.Contains(report.Err().Error(), forged) {
		t.Fatalf("verdict %v", report.Err())
	}
	if clean, _ := VerifyApprovals(dir, audit.NewMemory()); clean.Err() == nil {
		t.Fatal("verification under another key passed")
	}
}

// trustingVerifier accepts any decision: the forger's own store write.
type trustingVerifier struct{}

func (trustingVerifier) Verify(approval.Approval, approval.Decision) error { return nil }
