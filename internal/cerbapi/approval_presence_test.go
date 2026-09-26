package cerbapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/presence"
	"github.com/hollis-labs/cerberus/internal/presence/presencetest"
	"github.com/hollis-labs/cerberus/internal/redact"
)

const consoleOrigin = "http://localhost:4783"

// passkeyRoutes is a broker with a passkey service behind the socket's
// approvals routes, and a passkey enrolled through them.
func passkeyRoutes(t *testing.T, channel string) (post func(Principal, string, any) *httptest.ResponseRecorder, a approval.Approval, key *presencetest.Authenticator, broker *Broker) {
	t.Helper()
	broker, a, sink := pendingApproval(t, agentMCP, channel)
	SetBroker(broker)
	SetPresence(presence.New(t.TempDir(), sink, presence.Options{Origins: func() []string { return []string{consoleOrigin} }}))
	t.Cleanup(func() { SetBroker(nil); presencePoint.Store(nil) })
	s := &SocketServer{}
	post = func(p Principal, path string, body any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(data))).WithContext(as(p))
		w, r := BeginHTTPRequest(rec, req, SurfaceSocket)
		s.handleApprovals(w, r.WithContext(WithPrincipal(r.Context(), p)))
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder, v any) {
		t.Helper()
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), v) != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	token, digest := presence.NewEnrollToken()
	decode(post(humanCLI, "/approvals/keys/enroll-allow", EnrollAllowArgs{Digest: digest}), &struct{}{})
	var begin EnrollCeremony
	decode(post(humanWeb, "/approvals/keys/register/begin", EnrollBeginArgs{Token: token, Origin: consoleOrigin}), &begin)
	key = presencetest.New(t, presence.RPID, consoleOrigin)
	var info presence.KeyInfo
	decode(post(humanWeb, "/approvals/keys/register/finish", EnrollFinishArgs{Ceremony: begin.Ceremony, Attestation: key.Register(t, optionsJSON(t, begin.Creation))}), &info)
	return post, a, key, broker
}

// assertionFor runs the approval challenge through the socket and signs it
// with key, as the console's approvals page does.
func assertionFor(t *testing.T, post func(Principal, string, any) *httptest.ResponseRecorder, id string, key *presencetest.Authenticator) json.RawMessage {
	t.Helper()
	rec := post(humanWeb, "/approvals/"+id+"/challenge", ApprovalChallengeArgs{Origin: consoleOrigin})
	var c PasskeyCeremony
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
		t.Fatalf("challenge: %d %s", rec.Code, rec.Body.String())
	}
	out, _ := json.Marshal(map[string]any{"ceremony": c.Ceremony, "credential": key.Assert(t, optionsJSON(t, c.Options))})
	return out
}

func optionsJSON(t *testing.T, o WebAuthnOptions) json.RawMessage {
	t.Helper()
	raw, err := o.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The options reach the browser whole through the socket's response
// redaction: WebAuthn's allowCredentials once came back as "[REDACTED]",
// which the software authenticator here ignores and a browser refuses.
func TestPasskeyOptionsSurviveResponseRedaction(t *testing.T) {
	post, a, _, _ := passkeyRoutes(t, approval.ChannelOutOfBand)
	rec := post(humanWeb, "/approvals/"+a.ID+"/challenge", ApprovalChallengeArgs{Origin: consoleOrigin})
	var c PasskeyCeremony
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
		t.Fatalf("challenge: %d %s", rec.Code, rec.Body.String())
	}
	var opts struct {
		PublicKey struct {
			AllowCredentials []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"allowCredentials"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(optionsJSON(t, c.Options), &opts); err != nil {
		t.Fatal(err)
	}
	creds := opts.PublicKey.AllowCredentials
	if len(creds) != 1 || creds[0].Type != "public-key" || creds[0].ID == redact.Marker {
		t.Fatalf("allowCredentials = %+v", creds)
	}
	if _, err := base64.RawURLEncoding.DecodeString(creds[0].ID); err != nil {
		t.Fatalf("credential id %q is not base64url: %v", creds[0].ID, err)
	}

	// And a second enrollment, whose options carry excludeCredentials and
	// the enrolled key's allowCredentials.
	token, digest := presence.NewEnrollToken()
	post(humanCLI, "/approvals/keys/enroll-allow", EnrollAllowArgs{Digest: digest})
	rec = post(humanWeb, "/approvals/keys/register/begin", EnrollBeginArgs{Token: token, Origin: consoleOrigin})
	var begin EnrollCeremony
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &begin) != nil || begin.Authorize == "" {
		t.Fatalf("second enrollment: %d %s", rec.Code, rec.Body.String())
	}
	for _, o := range []WebAuthnOptions{begin.Creation, begin.Authorize} {
		if raw := optionsJSON(t, o); strings.Contains(string(raw), redact.Marker) {
			t.Fatalf("options were redacted: %s", raw)
		}
	}
}

// Out of band, an approve counts with an enrolled passkey's assertion: it is
// sealed into the decision, names the key, and is verified again when the
// approval is used.
func TestOutOfBandApproveWithAnEnrolledPasskey(t *testing.T) {
	post, a, key, broker := passkeyRoutes(t, approval.ChannelOutOfBand)
	rec := post(humanWeb, "/approvals/"+a.ID+"/decide", ApprovalDecisionArgs{Approve: true, Assertion: assertionFor(t, post, a.ID, key)})
	var got approval.Approval
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Status != approval.Approved || got.Decision.KeyFingerprint == "" {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := broker.Consume(context.Background(), a.ID, approval.ConsumeCheck{Connector: a.Connector, Operation: a.Operation, Principal: a.Principal,
		ArgsDigest: a.ArgsDigest, PlanHash: a.PlanHash, OperationID: audit.NewID()}); err != nil {
		t.Fatalf("consume: %v", err)
	}
}

// A passkey that is not enrolled does not approve, a replayed decide does
// not approve again, and an assertion made for one approval does not
// approve another.
func TestOutOfBandApproveRefusesOtherPasskeysAndReplays(t *testing.T) {
	post, a, key, broker := passkeyRoutes(t, approval.ChannelOutOfBand)
	stranger := presencetest.New(t, presence.RPID, consoleOrigin)
	rec := post(humanWeb, "/approvals/"+a.ID+"/decide", ApprovalDecisionArgs{Approve: true, Assertion: assertionFor(t, post, a.ID, stranger)})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an unenrolled passkey: %d %s", rec.Code, rec.Body.String())
	}
	if msg := rec.Body.String(); redact.Text(msg) != msg {
		t.Fatalf("redaction rewrote the refusal: %q", redact.Text(msg))
	}

	good := assertionFor(t, post, a.ID, key)
	if rec = post(humanWeb, "/approvals/"+a.ID+"/decide", ApprovalDecisionArgs{Approve: true, Assertion: good}); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if rec = post(humanWeb, "/approvals/"+a.ID+"/decide", ApprovalDecisionArgs{Approve: true, Assertion: good}); rec.Code == http.StatusOK {
		t.Fatalf("a replayed decide was accepted: %s", rec.Body.String())
	}

	other, err := broker.Request(context.Background(), audit.Record{Kind: audit.KindIntent, OperationID: audit.NewID(), Principal: agentMCP, Connector: "docker",
		Operation: "stop", Target: a.Target, ArgsDigest: "digest"}, policy.Result{Decision: policy.Approve}, approval.ChannelOutOfBand, approval.ScopeOnce, a.ExpiresAt.Sub(a.CreatedAt), "sha256:plan")
	if err != nil {
		t.Fatal(err)
	}
	if rec = post(humanWeb, "/approvals/"+other.ID+"/decide", ApprovalDecisionArgs{Approve: true, Assertion: good}); rec.Code == http.StatusOK {
		t.Fatalf("an assertion for %s approved %s", a.ID, other.ID)
	}
	if got, _ := broker.Get(other.ID); got.Status != approval.Pending {
		t.Fatalf("status = %s", got.Status)
	}
}

// Enrollment tokens come from the socket's CLI caller; a spent token and a
// console origin that is not running refuse.
func TestEnrollmentRoutesRefuse(t *testing.T) {
	post, _, _, _ := passkeyRoutes(t, approval.ChannelOutOfBand)
	rec := post(humanWeb, "/approvals/keys/register/begin", EnrollBeginArgs{Token: "spent", Origin: consoleOrigin})
	if rec.Code != http.StatusConflict {
		t.Fatalf("an unknown token: %d %s", rec.Code, rec.Body.String())
	}
	token, digest := presence.NewEnrollToken()
	if rec = post(humanCLI, "/approvals/keys/enroll-allow", EnrollAllowArgs{Digest: "not-a-digest"}); rec.Code != http.StatusConflict {
		t.Fatalf("a malformed digest: %d %s", rec.Code, rec.Body.String())
	}
	post(humanCLI, "/approvals/keys/enroll-allow", EnrollAllowArgs{Digest: digest})
	if rec = post(humanWeb, "/approvals/keys/register/begin", EnrollBeginArgs{Token: token, Origin: "http://evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("another origin: %d %s", rec.Code, rec.Body.String())
	}
	if redact.Text(rec.Body.String()) != rec.Body.String() {
		t.Fatalf("redaction rewrote the refusal: %q", redact.Text(rec.Body.String()))
	}
}
