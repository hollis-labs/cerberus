// Package approval is the approval broker's core: an append-only event store
// under ~/.cerberus/approvals/, folded into state on open, and the lifecycle
// an approval moves through
// (docs/plans/live-systems-security-target.md, P3 design §1).
//
//	pending  -> approved | denied | expired
//	approved -> consumed | expired | revoked
//
// The store is not trusted on its own word. It lives in files the operator's
// uid can write, and so can every agent that uid runs (§0). A line in the file
// that says "approved" proves nothing by itself: an out-of-band decision
// carries the approver's presence assertion, and Consume verifies it
// (PresenceVerifier, P3-4). A tty_confirm decision is never stored at all; it
// is made and used on the one call it approves.
//
// Consume is write-ahead: the consumed event is durable before the operation
// runs, so a crash between them leaves the approval spent and the operation
// not run. That is the safe side — nothing can run twice on one approval.
package approval

import (
	"encoding/json"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
)

// Status is where an approval is in its lifecycle.
type Status string

const (
	Pending  Status = "pending"
	Approved Status = "approved"
	Denied   Status = "denied"
	Expired  Status = "expired"
	Consumed Status = "consumed"
	Revoked  Status = "revoked"
)

// Terminal reports a status nothing moves out of.
func (s Status) Terminal() bool {
	return s == Denied || s == Expired || s == Consumed || s == Revoked
}

// Channels an approval can be met through (section 5).
const (
	ChannelTTYConfirm = "tty_confirm"
	ChannelOutOfBand  = "out_of_band"
	ChannelElicit     = "elicit"
	// ChannelBreakGlass is a person breaking glass on their own call, on a
	// target that does not need a passkey for it (P3-5b).
	ChannelBreakGlass = "break_glass"
)

// Scopes (section 5, design §5).
const (
	ScopeOnce    = "once"
	ScopeSession = "session"
	ScopeWindow  = "window"
)

// Approval is one request for approval and everything that happened to it.
type Approval struct {
	ID     string `json:"id"`
	Status Status `json:"status"`

	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`

	// What is to be approved: who asked, the operation, its target, the
	// keyed digest of its arguments and the plan it would run (P3-2).
	Principal  audit.Principal `json:"principal"`
	Connector  string          `json:"connector"`
	Operation  string          `json:"operation"`
	Effect     string          `json:"effect,omitempty"`
	Target     audit.Target    `json:"target"`
	ArgsDigest string          `json:"args_digest"`
	PlanHash   string          `json:"plan_hash,omitempty"`
	// Shown is what the approver is shown: the plan the approval binds to
	// and the call's arguments, redacted. Without it an approval is decided
	// on an operation name and a hash, which proves presence and not
	// consent (H3).
	Shown *Shown `json:"shown,omitempty"`

	// Why approval is needed: the rule that decided, and how it may be met.
	Rule   string `json:"rule,omitempty"`
	Reason string `json:"reason,omitempty"`
	// ExpiredReason is why an approval expired before its time: an
	// upgrade that no longer accepts its proof, say.
	ExpiredReason string `json:"expired_reason,omitempty"`
	Channel       string `json:"channel"`
	Scope         string `json:"scope"`
	Approvers     int    `json:"approvers,omitempty"`

	// RequestOperationID is the audit operation id of the call that asked.
	RequestOperationID string `json:"request_operation_id,omitempty"`

	Decision *Decision `json:"decision,omitempty"`

	ConsumedAt          time.Time `json:"consumed_at,omitzero"`
	ConsumedOperationID string    `json:"consumed_operation_id,omitempty"`

	RevokedAt time.Time        `json:"revoked_at,omitzero"`
	RevokedBy *audit.Principal `json:"revoked_by,omitempty"`

	// A grant (scope session or window, P3-5) is valid for TTL from when it
	// is approved, and counts its uses; it is not spent by one.
	// BreakGlass marks a person getting past an approve decision on their
	// own call, and why (P3-5b). It stays a follow-up until acknowledged.
	BreakGlass *BreakGlass `json:"break_glass,omitempty"`

	TTL                 time.Duration `json:"ttl,omitempty"`
	Uses                int           `json:"uses,omitempty"`
	LastUsedAt          time.Time     `json:"last_used_at,omitzero"`
	LastUsedOperationID string        `json:"last_used_operation_id,omitempty"`
}

// Shown is what an approver sees of the call they are asked to approve.
//
// Plan is the plan the approval binds to (its hash is PlanHash) and
// Arguments the call's arguments, both as JSON rendered through the request's
// redaction scope: credential values the request resolved are removed, and
// the rules run over the text. Untrusted names, as JSON pointers into Shown
// (for example "/arguments" or "/plan/preview"), the parts Cerberus did not
// compose: an agent wrote the arguments, and a preview echoes them. An
// approver reads those as claims to check, not as Cerberus's description.
type Shown struct {
	Plan      json.RawMessage `json:"plan,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Untrusted []string        `json:"untrusted,omitempty"`
	// Truncated says the rendering was over ShownMaxBytes and was cut; the
	// hash still binds the whole plan.
	Truncated bool `json:"truncated,omitempty"`
}

// ShownMaxBytes bounds what an approval stores for its approver.
const ShownMaxBytes = 32 << 10

// BreakGlass is a break-glass use: the reason given, the target typed, and
// the operator's acknowledgment of the follow-up it opens.
type BreakGlass struct {
	Reason  string           `json:"reason"`
	Typed   string           `json:"typed"`
	AckedAt time.Time        `json:"acked_at,omitzero"`
	AckedBy *audit.Principal `json:"acked_by,omitempty"`
	AckNote string           `json:"ack_note,omitempty"`
}

// IsGrant reports a session or window approval: reusable until it expires
// or is revoked.
func (a Approval) IsGrant() bool { return a.Scope == ScopeSession || a.Scope == ScopeWindow }

// Decision is who approved or denied, how, and with what proof.
type Decision struct {
	Approve bool            `json:"approve"`
	By      audit.Principal `json:"by"`
	At      time.Time       `json:"at"`
	Surface string          `json:"surface,omitempty"`
	Adapter string          `json:"adapter,omitempty"`
	Reason  string          `json:"reason,omitempty"`
	// KeyFingerprint and Assertion are an out-of-band decision's presence
	// proof (P3-4): which enrolled key, and its signature over the
	// approval. Consume verifies it; the store's word is not enough.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	Assertion      []byte `json:"assertion,omitempty"`
}

// ApproveWith is the command that decides this approval, for a caller told
// to wait for it.
func (a Approval) ApproveWith() string {
	return "cerberus approvals approve " + a.ID
}

// viewAt is the approval as it reads at now: a pending or approved one past
// its expiry reads expired, before the sweeper has written that down.
func (a Approval) viewAt(now time.Time) Approval {
	if (a.Status == Pending || a.Status == Approved) && !a.ExpiresAt.IsZero() && !now.Before(a.ExpiresAt) {
		a.Status = Expired
	}
	return a
}
