package eventexport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
)

var at = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func mapAudit1(t *testing.T, rec audit.Record) *Mapped {
	t.Helper()
	ms := MapAudit(rec)
	if len(ms) != 1 {
		t.Fatalf("%s mapped to %d envelopes, want 1", rec.Kind, len(ms))
	}
	return &ms[0]
}

func payloadOf(t *testing.T, m *Mapped) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(m.Envelope.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAuditKindsThatAreNotExportedAsAuditRecorded(t *testing.T) {
	for _, kind := range []string{
		audit.KindChainStart, audit.KindFileStart,
		// the approval store is the authority for these four; exporting them
		// here too would report the same fact twice
		audit.KindApprovalRequested, audit.KindApprovalDecided, audit.KindApprovalExpired, audit.KindApprovalRevoked,
	} {
		if ms := MapAudit(audit.Record{Seq: 1, Kind: kind, Time: at, ApprovalID: "apr_1"}); len(ms) != 0 {
			t.Errorf("%s was exported as %s", kind, ms[0].Envelope.Kind)
		}
	}
}

// consumed, used, acked and reanchored approval-store events map to no event.
func TestApprovalStoreEventsThatMapToNoEvent(t *testing.T) {
	for _, typ := range []string{approval.EventConsumed, approval.EventUsed, approval.EventAcked, approval.EventReanchored} {
		if m := MapApproval(approval.Event{Seq: 5, Type: typ, ApprovalID: "apr_1", Time: at}); m != nil {
			t.Errorf("%s was exported as %s", typ, m.Envelope.Kind)
		}
	}
	// The same facts reach the stream through the audit chain.
	m := mapAudit1(t, audit.Record{Seq: 9, Kind: audit.KindApprovalConsumed, Time: at, ApprovalID: "apr_1", OperationID: "op_1", Hash: "sha256:abc"})
	if m == nil || m.Envelope.Kind != "audit.recorded" || payloadOf(t, m)["action"] != audit.KindApprovalConsumed {
		t.Fatalf("approval_consumed = %+v", m)
	}
}

func TestIntentIsPendingNotSuccessAndCarriesTheOriginalChainDigest(t *testing.T) {
	m := mapAudit1(t, audit.Record{Seq: 3, ID: "rec1", Kind: audit.KindIntent, Time: at, OperationID: "op_1", Hash: "sha256:deadbeef",
		Principal: audit.Principal{Kind: "agent", Subject: "agent:builder", AuthMethod: "bearer", TokenID: "tok1"}})
	if m == nil || m.Key != "a/3/deadbeef" {
		t.Fatalf("mapped = %+v", m)
	}
	p := payloadOf(t, m)
	if p["outcome"] != "pending" || p["action"] != "intent" || p["chain_digest"] != "sha256:deadbeef" || p["ref"] != "op_1" {
		t.Fatalf("payload = %v", p)
	}
	e := m.Envelope
	if e.Kind != "audit.recorded" || e.Subject != "msg://operation/cerberus/op_1" || e.Source.Channel != "audit" || e.Class.Category != "audit" || e.Class.PayloadSchema != "urn:hollis-labs:events:audit.recorded@1" {
		t.Fatalf("envelope = %+v", e)
	}
	if e.Corr == nil || e.Corr.CorrelationID != "op_1" {
		t.Fatalf("corr = %+v", e.Corr)
	}
	out := mapAudit1(t, audit.Record{Seq: 4, Kind: audit.KindOutcome, Time: at, OperationID: "op_1", OutcomeCode: "denied_by_policy"})
	if payloadOf(t, out)["outcome"] != "denied_by_policy" || out.Envelope.Class.Severity != "warning" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestActorIsTokenVerifiedOnlyForAVerifiedBearer(t *testing.T) {
	cases := map[string]struct {
		p    audit.Principal
		want *Actor
	}{
		"verified bearer, msg subject":     {audit.Principal{Kind: "agent", Subject: "msg://agent/agent-mux/agt_x", AuthMethod: "bearer"}, &Actor{URN: "msg://agent/agent-mux/agt_x", Kind: "agent", Verified: "token"}},
		"verified bearer, non-URN subject": {audit.Principal{Kind: "agent", Subject: "agent:a", AuthMethod: "bearer"}, &Actor{URN: "agent:a", Kind: "agent", Verified: "claimed"}},
		"self reported":                    {audit.Principal{Kind: "agent", Subject: "agent:a", AuthMethod: "bearer", SelfReported: true}, &Actor{URN: "agent:a", Kind: "agent", Verified: "claimed"}},
		"client name only":                 {audit.Principal{Kind: "human", Client: "terminal"}, &Actor{URN: "terminal", Kind: "user", Verified: "claimed"}},
		"automation":                       {audit.Principal{Kind: audit.PrincipalAutomation, Client: "cerberus"}, &Actor{URN: "cerberus", Kind: "service", Verified: "claimed"}},
		"uid only":                         {audit.Principal{Kind: "human", Surface: "socket"}, nil},
		"nothing recorded":                 {audit.Principal{}, nil},
	}
	for name, c := range cases {
		got := actorOf(c.p)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: actor = %+v, want %+v", name, got, c.want)
		}
	}
}

func TestNothingSensitiveIsCopiedIntoAnEnvelope(t *testing.T) {
	rec := audit.Record{Seq: 7, ID: "r7", Kind: audit.KindGrantCreated, Time: at, ApprovalID: "apr_9", Hash: "sha256:h",
		ArgsDigest: "hmac-sha256:SECRETARGS", Note: "NOTETEXT", Reason: "REASONTEXT", PolicySnapshot: "POLICYSNAP",
		CredentialNames: []string{"github/SECRETNAME"}, Preview: "PREVIEWTEXT",
		Principal: audit.Principal{Kind: "human", Client: "cli", Session: "SESSIONID", Scopes: []string{"SCOPE"}}}
	m := mapAudit1(t, rec)
	if m == nil {
		t.Fatal("not exported")
	}
	text := string(m.Envelope.Payload) + m.Envelope.Subject
	for _, secret := range []string{"SECRETARGS", "NOTETEXT", "REASONTEXT", "POLICYSNAP", "SECRETNAME", "PREVIEWTEXT", "SESSIONID", "SCOPE"} {
		if strings.Contains(text, secret) {
			t.Errorf("payload leaks %s: %s", secret, text)
		}
	}
	req := MapApproval(approval.Event{Seq: 1, Type: approval.EventRequested, ApprovalID: "apr_9", Time: at, Approval: &approval.Approval{
		ID: "apr_9", Connector: "kubernetes", Operation: "delete_pod", ArgsDigest: "hmac:SECRETARGS", Reason: "REASONTEXT", Rule: "builtin.env-prod",
		Principal: audit.Principal{Kind: "agent", Subject: "agent:a", AuthMethod: "bearer"}, ExpiresAt: at.Add(time.Hour),
		Shown: &approval.Shown{}, Target: audit.Target{Resource: "RESOURCENAME"}}})
	text = string(req.Envelope.Payload)
	for _, secret := range []string{"SECRETARGS", "REASONTEXT", "RESOURCENAME"} {
		if strings.Contains(text, secret) {
			t.Errorf("approval payload leaks %s: %s", secret, text)
		}
	}
}

func TestApprovalLifecycleMapping(t *testing.T) {
	by := audit.Principal{Kind: "human", Subject: "user:chris", AuthMethod: "passkey"}
	requested := MapApproval(approval.Event{Seq: 1, Type: approval.EventRequested, ApprovalID: "apr_1", Time: at, Approval: &approval.Approval{
		ID: "apr_1", Connector: "kubernetes", Operation: "delete_pod", Rule: "builtin.env-prod", ExpiresAt: at.Add(time.Hour),
		Principal: audit.Principal{Kind: "agent", Subject: "agent:a", AuthMethod: "bearer"}}})
	p := payloadOf(t, requested)
	if requested.Envelope.Kind != "approval.requested" || p["request_id"] != "apr_1" || p["hitl_ref"] != "msg://approval/cerberus/apr_1" ||
		p["requester"] != "agent:a" || p["subject"] != "kubernetes.delete_pod" || p["class"] != "builtin.env-prod" || p["expires_at"] != "2026-10-16T13:00:00Z" {
		t.Fatalf("requested = %v", p)
	}
	if c := requested.Envelope.Class; c.Visibility != VisibilityOperator || c.Category != "approval" {
		t.Fatalf("class = %+v", c)
	}
	for _, c := range []struct {
		ev       approval.Event
		outcome  string
		resolver map[string]any
	}{
		{approval.Event{Seq: 2, Type: approval.EventDecided, ApprovalID: "apr_1", Time: at, Decision: &approval.Decision{Approve: true, By: by}}, "approved", map[string]any{"kind": "user", "id": "user:chris"}},
		{approval.Event{Seq: 3, Type: approval.EventDecided, ApprovalID: "apr_1", Time: at, Decision: &approval.Decision{Approve: false, By: by}}, "denied", map[string]any{"kind": "user", "id": "user:chris"}},
		{approval.Event{Seq: 4, Type: approval.EventExpired, ApprovalID: "apr_1", Time: at}, "expired", map[string]any{"kind": "service", "id": "cerberus"}},
		{approval.Event{Seq: 5, Type: approval.EventRevoked, ApprovalID: "apr_1", Time: at, Decision: &approval.Decision{By: by}}, "withdrawn", map[string]any{"kind": "user", "id": "user:chris"}},
		{approval.Event{Seq: 6, Type: approval.EventDecided, ApprovalID: "apr_1", Time: at, Decision: &approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Surface: "socket"}}}, "approved", nil},
	} {
		m := MapApproval(c.ev)
		p := payloadOf(t, m)
		if m.Envelope.Kind != "approval.resolved" || p["outcome"] != c.outcome || p["hitl_ref"] != "msg://approval/cerberus/apr_1" || p["request_id"] != "apr_1" {
			t.Errorf("seq %d = %v", c.ev.Seq, p)
		}
		got, _ := p["resolver"].(map[string]any)
		if (got == nil) != (c.resolver == nil) || (got != nil && (got["kind"] != c.resolver["kind"] || got["id"] != c.resolver["id"])) {
			t.Errorf("seq %d resolver = %v, want %v", c.ev.Seq, got, c.resolver)
		}
		if m.Key != fmt.Sprintf("p/%d/", c.ev.Seq) {
			t.Errorf("key = %s", m.Key)
		}
	}
	// An expiry sweep is never a person.
	expired := MapApproval(approval.Event{Seq: 4, Type: approval.EventExpired, ApprovalID: "apr_1", Time: at})
	if expired.Envelope.Actor != nil {
		t.Fatalf("an expiry has an actor: %+v", expired.Envelope.Actor)
	}
}

func TestAHealedTornWriteBecomesARaisedAndClearedPairAndNothingStaysOpen(t *testing.T) {
	ms := MapAudit(audit.Record{Seq: 11, ID: "r11", Kind: audit.KindChainBreak, Time: at})
	if len(ms) != 2 || ms[0].Envelope.Kind != "alert.raised" || ms[1].Envelope.Kind != "alert.cleared" || ms[0].Key == ms[1].Key {
		t.Fatalf("pair = %+v", ms)
	}
	for _, m := range ms {
		p := payloadOf(t, &m)
		if p["rule_id"] != RuleAuditTornWrite || p["coalesce_key"] != "audit-torn-write/11" || m.Envelope.Class.Severity != "notice" ||
			m.Envelope.Class.Visibility != VisibilityEnvironment || m.Envelope.Class.Category != "alert" {
			t.Fatalf("%s = %v %+v", m.Envelope.Kind, p, m.Envelope.Class)
		}
		ids, _ := p["source_event_ids"].([]any)
		if ids == nil || len(ids) != 0 {
			t.Fatalf("source_event_ids must be present and empty, got %v", p["source_event_ids"])
		}
	}
	// A reanchor clears the broken-chain alert, not the torn-write one.
	cleared := mapAudit1(t, audit.Record{Seq: 12, ID: "r12", Kind: audit.KindChainReanchored, Time: at})
	if cleared.Envelope.Kind != "alert.cleared" || payloadOf(t, cleared)["rule_id"] != RuleAuditChainBroken {
		t.Fatalf("reanchor = %+v", cleared.Envelope)
	}
}
