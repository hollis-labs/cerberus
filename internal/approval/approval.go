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

	// Why approval is needed: the rule that decided, and how it may be met.
	Rule      string `json:"rule,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Channel   string `json:"channel"`
	Scope     string `json:"scope"`
	Approvers int    `json:"approvers,omitempty"`

	// RequestOperationID is the audit operation id of the call that asked.
	RequestOperationID string `json:"request_operation_id,omitempty"`

	Decision *Decision `json:"decision,omitempty"`

	ConsumedAt          time.Time `json:"consumed_at,omitzero"`
	ConsumedOperationID string    `json:"consumed_operation_id,omitempty"`

	RevokedAt time.Time        `json:"revoked_at,omitzero"`
	RevokedBy *audit.Principal `json:"revoked_by,omitempty"`
}

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
