package gate

import (
	"context"
	"log/slog"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// Broker is the approval broker (P3-1): the event store, with an audit
// record for every transition. It lives in the daemon, the one writer; the
// in-process CLI has none, so it meets only tty_confirm itself (D3).
//
// Out-of-band decisions are verified by an approval.PresenceVerifier. The
// old build's was the passkey service; passkeys are dropped (decision 0.1)
// and the default here is OwnerSessionVerifier: an out-of-band decision
// counts only when it was made by the owner signed in to the console
// (DecideAs with an OwnerSession).
type Broker struct {
	store    *approval.Store
	sink     audit.Sink
	logger   *slog.Logger
	verifier approval.PresenceVerifier
}

// NewBroker opens the store in dir. What the fold found wrong is logged,
// never fatal: nothing in the store authorizes anything on its own word.
func NewBroker(sink audit.Sink, dir string) (*Broker, error) {
	if sink == nil {
		panic("gate: NewBroker requires an audit sink")
	}
	store, err := approval.Open(dir)
	if err != nil {
		return nil, err
	}
	b := &Broker{store: store, sink: sink, logger: slog.Default(), verifier: OwnerSessionVerifier{}}
	for _, p := range store.Problems {
		b.logger.Warn("approvals.store_problem", "problem", p)
	}
	return b, nil
}

// SetVerifier replaces the out-of-band proof check (nil keeps the current
// one). Tests use it; the daemon keeps the default OwnerSessionVerifier.
func (b *Broker) SetVerifier(v approval.PresenceVerifier) {
	if v != nil {
		b.verifier = v
	}
}

// List is every approval, newest first; Get is one.
func (b *Broker) List() []approval.Approval { return b.store.List() }

// Get returns one approval.
func (b *Broker) Get(id string) (approval.Approval, bool) { return b.store.Get(id) }

// Problems are what folding the store found wrong.
func (b *Broker) Problems() []string { return append([]string(nil), b.store.Problems...) }

// Request records a pending approval for the call behind intent.
// snap is the plan it binds to and what its approver is shown.
func (b *Broker) Request(ctx context.Context, intent audit.Record, res policy.Result, channel, scope string, ttl time.Duration, snap PlanSnapshot) (approval.Approval, error) {
	return b.request(ctx, intent, res, channel, scope, ttl, snap, nil)
}

func (b *Broker) request(ctx context.Context, intent audit.Record, res policy.Result, channel, scope string, ttl time.Duration, snap PlanSnapshot, bg *approval.BreakGlass) (approval.Approval, error) {
	a, err := b.store.Request(approval.Approval{
		Principal: intent.Principal, Connector: intent.Connector, Operation: intent.Operation, Effect: intent.Effect,
		Target: intent.Target, ArgsDigest: intent.ArgsDigest, PlanHash: snap.Hash, Shown: snap.Shown, Rule: decidingRule(res), Reason: res.Reason(),
		Channel: channel, Scope: scope, RequestOperationID: intent.OperationID, BreakGlass: bg,
	}, ttl)
	if err != nil {
		return approval.Approval{}, err
	}
	b.record(ctx, audit.KindApprovalRequested, a, intent.OperationID)
	return a, nil
}

// Decide records a decision, verifying an out-of-band one's proof (for the
// default verifier, the owner-session marker DecideAs sets).
func (b *Broker) Decide(ctx context.Context, id string, d approval.Decision) (approval.Approval, error) {
	a, err := b.store.Decide(id, d, b.verifier)
	if err != nil {
		return a, err
	}
	b.record(ctx, audit.KindApprovalDecided, a, "")
	if d.Approve && a.IsGrant() {
		b.record(ctx, audit.KindGrantCreated, a, "")
	}
	return a, nil
}

// Consume spends an approval on the call behind operationID, write-ahead:
// the broker's event and the audit record are written before the caller
// runs anything. Policy is re-evaluated by the caller and passed in (D8).
// An out-of-band approval is checked against the verification recorded
// with its decision; no fresh proof is asked for at consume.
func (b *Broker) Consume(ctx context.Context, id string, check approval.ConsumeCheck) (approval.Approval, error) {
	a, err := b.store.Consume(id, check, b.verifier)
	if err != nil {
		return a, err
	}
	if a.IsGrant() {
		// Named by id, a grant is used, not spent.
		b.recordUse(ctx, a, check.OperationID)
		return a, nil
	}
	b.record(ctx, audit.KindApprovalConsumed, a, check.OperationID)
	return a, nil
}

// UseGrant uses the active grant that covers the call, if one does
// (approval.ErrNotFound when none does), recorded as grant_used.
func (b *Broker) UseGrant(ctx context.Context, check approval.GrantCheck) (approval.Approval, error) {
	a, err := b.store.UseGrant(check, b.verifier)
	if err != nil {
		return a, err
	}
	b.recordUse(ctx, a, check.OperationID)
	return a, nil
}

// ActiveGrants are the grants usable now.
func (b *Broker) ActiveGrants() []approval.Approval { return b.store.ActiveGrants() }

// recordUse is a grant's grant_used record, marked when it is on a
// protected target.
func (b *Broker) recordUse(ctx context.Context, a approval.Approval, operationID string) {
	b.recordWith(ctx, audit.KindGrantUsed, a, operationID, func(ref *audit.ApprovalRef) {
		ref.Uses = a.Uses
		ref.GrantOnProtectedTarget = protectedTarget(auditTargetLabels(a.Target))
	})
}

// Revoke withdraws an approved approval.
func (b *Broker) Revoke(ctx context.Context, id string, by approval.Decision) (approval.Approval, error) {
	a, err := b.store.Revoke(id, by)
	if err != nil {
		return a, err
	}
	kind := audit.KindApprovalRevoked
	if a.IsGrant() {
		kind = audit.KindGrantRevoked
	}
	b.record(ctx, kind, a, "")
	return a, nil
}

// Sweep writes down the approvals that have expired.
func (b *Broker) Sweep(ctx context.Context) {
	expired, err := b.store.Sweep()
	for _, a := range expired {
		b.record(ctx, audit.KindApprovalExpired, a, "")
	}
	if err != nil {
		b.logger.Error("approvals.sweep_failed", "error", redact.Text(err.Error()))
	}
}

// RunSweeper sweeps every interval until ctx ends, and returns then, so a
// caller can stop it in order.
func (b *Broker) RunSweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.Sweep(ctx)
		}
	}
}

// record writes a broker transition to the audit log. The broker's own
// store is written first; a failure here is logged loudly, and the missing
// record shows as a store event with no audit record.
func (b *Broker) record(ctx context.Context, kind string, a approval.Approval, operationID string) {
	b.recordWith(ctx, kind, a, operationID, nil)
}

func (b *Broker) recordWith(ctx context.Context, kind string, a approval.Approval, operationID string, with func(*audit.ApprovalRef)) {
	ref := &audit.ApprovalRef{ID: a.ID, Status: string(a.Status), Channel: a.Channel, Scope: a.Scope, Rule: a.Rule, PlanHash: a.PlanHash, ExpiresAt: a.ExpiresAt}
	if a.Decision != nil {
		by := a.Decision.By
		ref.DecidedBy, ref.KeyFingerprint, ref.DecidedSameSurface = &by, a.Decision.KeyFingerprint, a.Decision.SameSurface
	}
	if with != nil {
		with(ref)
	}
	rec := audit.Record{Kind: kind, OperationID: operationID, Principal: principalFor(ctx, Spec{}), Connector: a.Connector,
		Operation: a.Operation, Effect: a.Effect, Target: a.Target, ArgsDigest: a.ArgsDigest, Approval: ref, Posture: audit.PostureSecure}
	if _, err := b.sink.Write(rec); err != nil {
		b.logger.Error("audit.write_failed", "kind", kind, "approval", a.ID, "error", redact.Text(err.Error()))
	}
}

// ApprovalList is the broker's approvals and what folding its store found
// wrong.
type ApprovalList struct {
	Approvals []approval.Approval `json:"approvals"`
	Problems  []string            `json:"problems,omitempty"`
}
