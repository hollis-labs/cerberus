package eventexport

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
)

// Payload schema ids: the full catalog ids (libs harness/core
// adapters/runtimeevents/catalog), not the short names.
const (
	schemaAuditRecorded     = "urn:hollis-labs:events:audit.recorded@1"
	schemaApprovalRequested = "urn:hollis-labs:events:approval.requested@1"
	schemaApprovalResolved  = "urn:hollis-labs:events:approval.resolved@1"
	schemaAlert             = "urn:hollis-labs:events:alert@1"
)

// Rule ids of the alerts Cerberus raises about its own export.
const (
	RuleAuditTornWrite     = "cerberus.audit_torn_write"
	RuleAuditChainBroken   = "cerberus.audit_chain_broken"
	RuleApprovalChainBreak = "cerberus.approval_chain_break"
	RuleAuditGap           = "cerberus.audit_gap"
	RuleScheduleJobFailed  = "cerberus.schedule_job_failed"
)

// Mapped is an envelope and the key that makes appending it idempotent.
type Mapped struct {
	Key      string
	Envelope Envelope
}

// MapAudit maps one audit chain record, or returns nil for a record that is
// not exported:
//
//   - chain_start and file_start are chain bookkeeping and stay local;
//   - approval_requested, approval_decided, approval_expired and approval_revoked
//     are not exported here because the approval store is the authority for
//     them and they are exported from it as approval.* (a second event would
//     report the same fact twice);
//   - chain_break is a torn write the sink already healed (the chain verifies
//     through it), so it becomes an alert.raised and an alert.cleared pair in the
//     same batch (rule cerberus.audit_torn_write, one coalesce key per record):
//     nothing stays open for a condition that is over;
//   - chain_reanchored becomes alert.cleared for cerberus.audit_chain_broken;
//   - every other kind becomes audit.recorded with the original kind as the
//     action and the record's own hash as chain_digest. The digest anchors the
//     original record; the exported payload is a projection and does not hash
//     to it.
//
// Payloads are built field by field. Arguments, plans, policy snapshots, notes,
// reasons and credential names are never copied.
func MapAudit(rec audit.Record) []Mapped {
	key := fmt.Sprintf("a/%d/%s", rec.Seq, hashTag(rec.Hash))
	switch rec.Kind {
	case audit.KindChainStart, audit.KindFileStart,
		audit.KindApprovalRequested, audit.KindApprovalDecided, audit.KindApprovalExpired, audit.KindApprovalRevoked:
		return nil
	case audit.KindChainBreak:
		subject, refs, coalesce := "msg://audit/cerberus/"+rec.ID, map[string]any{"audit_seq": rec.Seq}, fmt.Sprintf("audit-torn-write/%d", rec.Seq)
		labels := []string{"audit", "torn_write"}
		return []Mapped{
			{key + "/raised", alert("alert.raised", RuleAuditTornWrite, "a write to the audit chain was torn and healed; the chain resumes from the last complete record", "notice", rec.Time, subject, labels, refs, coalesce, ChannelAudit)},
			{key + "/cleared", alert("alert.cleared", RuleAuditTornWrite, "the torn audit write was healed", "notice", rec.Time, subject, labels, refs, coalesce, ChannelAudit)},
		}
	case audit.KindChainReanchored:
		return []Mapped{{key, alert("alert.cleared", RuleAuditChainBroken, "the audit chain was reanchored", "info", rec.Time, "msg://audit/cerberus/"+rec.ID,
			[]string{"audit", "reanchored"}, map[string]any{"audit_seq": rec.Seq}, "audit-chain-broken", ChannelAudit)}}
	}
	subjectType, subjectID, urn := auditSubject(rec)
	payload := map[string]any{
		"action":       rec.Kind,
		"outcome":      auditOutcome(rec),
		"chain_digest": rec.Hash,
	}
	if subjectID != "" {
		payload["subject"] = map[string]string{"type": subjectType, "id": subjectID}
	}
	if rec.OperationID != "" {
		payload["ref"] = rec.OperationID
	}
	env := Envelope{
		Kind:    "audit.recorded",
		Time:    rec.Time.UTC(),
		Subject: urn,
		Source:  Source{Channel: ChannelAudit},
		Corr:    corr(firstNonEmpty(rec.OperationID, rec.ApprovalID)),
		Actor:   actorOf(rec.Principal),
		Class:   &Class{Category: "audit", Severity: auditSeverity(rec), PayloadSchema: schemaAuditRecorded},
		Payload: mustJSON(payload),
	}
	return []Mapped{{key, env}}
}

func auditSubject(rec audit.Record) (typ, id, urn string) {
	switch {
	case rec.OperationID != "":
		return "operation", rec.OperationID, "msg://operation/cerberus/" + rec.OperationID
	case rec.ApprovalID != "":
		return "approval", rec.ApprovalID, "msg://approval/cerberus/" + rec.ApprovalID
	}
	return "", "", "msg://audit/cerberus/" + rec.ID
}

// auditOutcome is the outcome word of an audit.recorded. An intent has not run
// yet, so it is pending and never reads as success.
func auditOutcome(rec audit.Record) string {
	switch rec.Kind {
	case audit.KindIntent:
		return "pending"
	case audit.KindOutcome:
		return firstNonEmpty(rec.OutcomeCode, rec.Decision, "ok")
	}
	return "ok"
}

func auditSeverity(rec audit.Record) string {
	if rec.Kind == audit.KindOutcome && rec.OutcomeCode != "" && rec.OutcomeCode != "ok" {
		return "warning"
	}
	if rec.Kind == audit.KindBreakGlass {
		return "notice"
	}
	return "info"
}

// MapApproval maps one approval store event, or returns nil for an event that
// is not exported. requested, decided, expired and revoked become
// approval.requested and approval.resolved. consumed, used, acked and reanchored
// map to no event: consumption and grant use are audit records (audit.recorded
// from the chain), and a reanchor is store bookkeeping.
func MapApproval(ev approval.Event) *Mapped {
	key := fmt.Sprintf("p/%d/%s", ev.Seq, hashTag(ev.Hash))
	urn := "msg://approval/cerberus/" + ev.ApprovalID
	base := Envelope{
		Time:    ev.Time.UTC(),
		Subject: urn,
		Source:  Source{Channel: ChannelAudit},
		Corr:    corr(ev.ApprovalID),
	}
	class := func(schema string) *Class {
		return &Class{Category: "approval", Severity: "info", Visibility: VisibilityOperator, PayloadSchema: schema}
	}
	switch ev.Type {
	case approval.EventRequested:
		if ev.Approval == nil {
			return nil
		}
		a := ev.Approval
		payload := map[string]any{"request_id": a.ID, "hitl_ref": urn}
		setIf(payload, "class", a.Rule)
		setIf(payload, "requester", principalID(a.Principal))
		setIf(payload, "subject", strings.Trim(a.Connector+"."+a.Operation, "."))
		if !a.ExpiresAt.IsZero() {
			payload["expires_at"] = a.ExpiresAt.UTC().Format(time.RFC3339)
		}
		base.Kind, base.Actor, base.Class, base.Payload = "approval.requested", actorOf(a.Principal), class(schemaApprovalRequested), mustJSON(payload)
	case approval.EventDecided:
		if ev.Decision == nil {
			return nil
		}
		outcome := "denied"
		if ev.Decision.Approve {
			outcome = "approved"
		}
		base.Kind, base.Actor, base.Class = "approval.resolved", actorOf(ev.Decision.By), class(schemaApprovalResolved)
		base.Payload = mustJSON(resolved(ev.ApprovalID, urn, outcome, resolverOf(ev.Decision.By)))
	case approval.EventExpired:
		// An expiry sweep is the service resolving it, never a person.
		base.Kind, base.Class = "approval.resolved", class(schemaApprovalResolved)
		base.Payload = mustJSON(resolved(ev.ApprovalID, urn, "expired", map[string]string{"kind": "service", "id": "cerberus"}))
	case approval.EventRevoked:
		var by audit.Principal
		if ev.Decision != nil {
			by = ev.Decision.By
		}
		base.Kind, base.Actor, base.Class = "approval.resolved", actorOf(by), class(schemaApprovalResolved)
		base.Payload = mustJSON(resolved(ev.ApprovalID, urn, "withdrawn", resolverOf(by)))
	default: // consumed, used, acked, reanchored
		return nil
	}
	return &Mapped{key, base}
}

// hashTag is the first 16 hex digits of a record's hash. It is part of every
// deduplication key, so a store restored or rewritten with reused sequence
// numbers does not have its new records mistaken for ones already exported.
func hashTag(hash string) string {
	hash = strings.TrimPrefix(hash, "sha256:")
	if len(hash) > 16 {
		hash = hash[:16]
	}
	return hash
}

func resolved(id, urn, outcome string, resolver map[string]string) map[string]any {
	payload := map[string]any{"request_id": id, "hitl_ref": urn, "outcome": outcome}
	if resolver != nil {
		payload["resolver"] = resolver
	}
	return payload
}

// resolverOf names who resolved an approval, or nil when Cerberus recorded no
// identity for them: both kind and id are required, so a partial one is omitted.
func resolverOf(p audit.Principal) map[string]string {
	id := principalID(p)
	if id == "" {
		return nil
	}
	return map[string]string{"kind": actorKind(p), "id": id}
}

// principalID is the identity string Cerberus recorded for a principal, exactly
// as recorded: the verified bearer's subject, else the client name. A UID is not
// turned into a name.
func principalID(p audit.Principal) string {
	return firstNonEmpty(p.Subject, p.Client)
}

func actorKind(p audit.Principal) string {
	switch p.Kind {
	case audit.PrincipalAutomation:
		return "service"
	case "agent":
		return "agent"
	}
	return "user"
}

// actorOf is the envelope actor for a principal, or nil when Cerberus recorded
// no identity string (rather than building one from a UID). Verified is token
// only for a verified bearer; self-reported, UID-only and unverified principals
// are claimed.
func actorOf(p audit.Principal) *Actor {
	id := principalID(p)
	if id == "" {
		return nil
	}
	verified := "claimed"
	// token only for a verified bearer whose subject is a msg:// URN: the hub
	// projection drops verified=token on any other subject, so claiming it there
	// would be a statement the stream does not keep.
	if p.Subject != "" && p.AuthMethod != "" && !p.SelfReported && strings.HasPrefix(p.Subject, "msg://") {
		verified = "token"
	}
	return &Actor{URN: id, Kind: actorKind(p), Verified: verified}
}

// alert builds an alert.raised or alert.cleared envelope. source_event_ids is
// empty because every source here is local and was never published; the local
// references go in source_refs.
func alert(kind, rule, summary, severity string, at time.Time, subject string, labels []string, refs map[string]any, coalesce, channel string) Envelope {
	payload := map[string]any{
		"rule_id":          rule,
		"source_event_ids": []string{},
		"labels":           labels,
		"summary":          summary,
		"source_refs":      refs,
	}
	if coalesce != "" {
		payload["coalesce_key"] = coalesce
	}
	return Envelope{
		Kind:    kind,
		Time:    at.UTC(),
		Subject: subject,
		Source:  Source{Channel: channel},
		Class:   &Class{Category: "alert", Severity: severity, Visibility: VisibilityEnvironment, PayloadSchema: schemaAlert, Labels: labels},
		Payload: mustJSON(payload),
	}
}

func corr(id string) *Corr {
	if id == "" {
		return nil
	}
	return &Corr{CorrelationID: id}
}

func setIf(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("eventexport: marshal %T: %v", v, err))
	}
	return b
}
