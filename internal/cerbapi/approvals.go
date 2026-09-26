package cerbapi

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
)

// Broker is the approval broker (P3-1): the event store, with an audit
// record for every transition. It lives in the daemon, the one writer; the
// in-process CLI has none, so it meets only tty_confirm itself (D3).
type Broker struct {
	store    *approval.Store
	sink     audit.Sink
	logger   *slog.Logger
	verifier approval.PresenceVerifier
}

// DefaultApprovalTTL is how long a request waits for a decision, and an
// approval for its use, when the rule does not say.
const DefaultApprovalTTL = time.Hour

// NewBroker opens the store in dir. What the fold found wrong is logged,
// never fatal: nothing in the store authorizes anything on its own word.
func NewBroker(sink audit.Sink, dir string) (*Broker, error) {
	if sink == nil {
		panic("cerbapi: NewBroker requires an audit sink")
	}
	store, err := approval.Open(dir)
	if err != nil {
		return nil, err
	}
	b := &Broker{store: store, sink: sink, logger: slog.Default(), verifier: approval.NoPresence{}}
	for _, p := range store.Problems {
		b.logger.Warn("approvals.store_problem", "problem", p)
	}
	return b, nil
}

// SetPresenceVerifier installs the out-of-band proof check (P3-4).
func (b *Broker) SetPresenceVerifier(v approval.PresenceVerifier) {
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
func (b *Broker) Request(ctx context.Context, intent audit.Record, res policy.Result, channel, scope string, ttl time.Duration, planHash string) (approval.Approval, error) {
	a, err := b.store.Request(approval.Approval{
		Principal: intent.Principal, Connector: intent.Connector, Operation: intent.Operation, Effect: intent.Effect,
		Target: intent.Target, ArgsDigest: intent.ArgsDigest, PlanHash: planHash, Rule: decidingRule(res), Reason: res.Reason(),
		Channel: channel, Scope: scope, RequestOperationID: intent.OperationID,
	}, ttl)
	if err != nil {
		return approval.Approval{}, err
	}
	b.record(ctx, audit.KindApprovalRequested, a, intent.OperationID)
	return a, nil
}

// Decide records a decision, verifying an out-of-band one's presence proof.
func (b *Broker) Decide(ctx context.Context, id string, d approval.Decision) (approval.Approval, error) {
	a, err := b.store.Decide(id, d, b.verifier)
	if err != nil {
		return a, err
	}
	b.record(ctx, audit.KindApprovalDecided, a, "")
	return a, nil
}

// Consume spends an approval on the call behind operationID, write-ahead:
// the broker's event and the audit record are written before the caller
// runs anything. Policy is re-evaluated by the caller and passed in (D8).
func (b *Broker) Consume(ctx context.Context, id string, check approval.ConsumeCheck) (approval.Approval, error) {
	a, err := b.store.Consume(id, check, b.verifier)
	if err != nil {
		return a, err
	}
	b.record(ctx, audit.KindApprovalConsumed, a, check.OperationID)
	return a, nil
}

// Revoke withdraws an approved approval.
func (b *Broker) Revoke(ctx context.Context, id string, by approval.Decision) (approval.Approval, error) {
	a, err := b.store.Revoke(id, by)
	if err != nil {
		return a, err
	}
	b.record(ctx, audit.KindApprovalRevoked, a, "")
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

// RunSweeper sweeps every interval until ctx ends.
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
	ref := &audit.ApprovalRef{ID: a.ID, Status: string(a.Status), Channel: a.Channel, Scope: a.Scope, Rule: a.Rule, PlanHash: a.PlanHash, ExpiresAt: a.ExpiresAt}
	if a.Decision != nil {
		by := a.Decision.By
		ref.DecidedBy, ref.KeyFingerprint = &by, a.Decision.KeyFingerprint
	}
	rec := audit.Record{Kind: kind, OperationID: operationID, Principal: principalFor(ctx, auditSpec{}), Connector: a.Connector,
		Operation: a.Operation, Effect: a.Effect, Target: a.Target, ArgsDigest: a.ArgsDigest, Approval: ref, Posture: audit.PostureSecure}
	if _, err := b.sink.Write(rec); err != nil {
		b.logger.Error("audit.write_failed", "kind", kind, "approval", a.ID, "error", redact.Text(err.Error()))
	}
}

// The process's broker: the daemon installs one; the in-process CLI has
// none.
var brokerPoint atomic.Pointer[Broker]

// SetBroker installs the process's broker (nil removes it).
func SetBroker(b *Broker) { brokerPoint.Store(b) }

// ProcessBroker is the installed broker, or nil.
func ProcessBroker() *Broker { return brokerPoint.Load() }

// Enforcement says which operations policy is enforced for. P3-7 builds it
// from the applied snapshot's enforcement section; until then nothing
// installs one, and policy stays in shadow: recorded, never enforced.
type Enforcement interface {
	Enforced(policy.Request) bool
}

var enforcementPoint atomic.Pointer[enforcementHolder]

type enforcementHolder struct{ e Enforcement }

// SetEnforcement installs which operations are enforced (nil: none).
func SetEnforcement(e Enforcement) {
	if e == nil {
		enforcementPoint.Store(nil)
		return
	}
	enforcementPoint.Store(&enforcementHolder{e: e})
}

func enforced(req policy.Request) bool {
	h := enforcementPoint.Load()
	return h != nil && h.e.Enforced(req)
}

// beginGated is where a gated operation starts: its intent record and
// shadow decision (beginAudit), then, where enforcement is on for it, the
// decision enforced. Every error it returns is already coded for the
// caller. A refusal is recorded as the call's outcome before it returns.
// Where enforcement is off — everywhere, until P3-7 — this is beginAudit
// and nothing more: shadow mode is unchanged.
func beginGated(ctx context.Context, sink audit.Sink, logger *slog.Logger, spec auditSpec) (*auditCall, error) {
	call, err := beginAudit(ctx, sink, logger, spec)
	args := ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}
	if err != nil {
		return nil, externalConnectorError(args, ExternalConnectorAuditUnavailable, err)
	}
	if spec.automation {
		return call, nil
	}
	_, resolved := auditTarget(spec)
	req := policyRequest(ctx, spec, resolved)
	if !enforced(req) {
		return call, nil
	}
	if refusal := enforceDecision(ctx, call, spec, req); refusal != nil {
		call.finish(refusal)
		return nil, refusal
	}
	return call, nil
}

// enforceDecision is the enforced decision for a call: nil to let it run, or
// the coded refusal. Approval by id (P3-2) and on the call itself
// (tty_confirm, P3-3) come later; here an approve decision asks the broker
// for an approval and answers approval_pending.
func enforceDecision(ctx context.Context, call *auditCall, spec auditSpec, req policy.Request) error {
	res := PolicyDecisionPoint().Authorize(req)
	args := ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}
	switch res.Decision {
	case policy.Allow:
		// A policy that relaxed to allow since the approval was given still
		// spends it, so the record shows it was used (D8).
		if spec.approvalID != "" {
			return consumeApproval(ctx, call, spec)
		}
		return nil
	case policy.DryRunOnly:
		if req.DryRun {
			return nil
		}
		return externalConnectorError(args, ExternalConnectorPolicyDenied,
			redact.Guidance("policy allows only a dry run of %s %s (rule %s); run it with --dry-run", spec.connector, spec.operation, decidingRule(res)))
	case policy.Approve:
		if req.DryRun {
			return nil
		}
		if spec.approvalID != "" {
			return consumeApproval(ctx, call, spec)
		}
		return requestApproval(ctx, call, spec, req, res)
	case policy.Deny:
	}
	reason := res.Reason()
	if reason == "" {
		reason = "no rule allows it"
	}
	return externalConnectorError(args, ExternalConnectorPolicyDenied,
		redact.Guidance("policy denies %s %s: %s (rule %s); `cerberus policy explain %s.%s` shows every rule that matched",
			spec.connector, spec.operation, reason, decidingRule(res), spec.connector, spec.operation))
}

// requestApproval asks the process's broker for an approval and answers
// approval_pending with how to decide it. With no broker — the in-process
// CLI without a daemon — an approval that has to come out of band cannot be
// asked for at all, so the answer names how to start the daemon (D3).
func requestApproval(ctx context.Context, call *auditCall, spec auditSpec, req policy.Request, res policy.Result) error {
	args := ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}
	channel, scope, ttl := approvalTerms(req.Target, res)
	broker := ProcessBroker()
	if broker == nil {
		if channel == approval.ChannelTTYConfirm {
			return externalConnectorError(args, ExternalConnectorApprovalRequired,
				redact.Guidance("%s %s needs your confirmation (rule %s): run it from an interactive terminal, where Cerberus shows the plan and asks you to type the target", spec.connector, spec.operation, decidingRule(res)))
		}
		return externalConnectorError(args, ExternalConnectorApprovalPending,
			redact.Guidance("%s %s needs an out-of-band approval (rule %s), and only the daemon can hold one; start it with `cerberus daemon`, then retry", spec.connector, spec.operation, decidingRule(res)))
	}
	planHash, err := specPlanHash(ctx, spec)
	if err != nil {
		return externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.GuidanceWrap(err, "%s %s needs approval, and approval binds to a plan, which could not be computed", spec.connector, spec.operation))
	}
	a, err := broker.Request(ctx, call.intent, res, channel, scope, ttl, planHash)
	if err != nil {
		return externalConnectorError(args, ExternalConnectorAuditUnavailable, err)
	}
	return approvalPendingError(args, a)
}

// specPlanHash is the lane's plan for the call, hashed. A lane without a
// plan function binds its approvals to the arguments alone.
func specPlanHash(ctx context.Context, spec auditSpec) (string, error) {
	if spec.plan == nil {
		return "", nil
	}
	p, err := spec.plan(ctx)
	if err != nil {
		return "", err
	}
	return p.Hash()
}

// consumeApproval spends the call's approval, write-ahead, after
// recomputing its plan the same way it was computed when the approval was
// asked for (I6). What the call runs under is written on its records: the
// outcome carries approval_id and plan_hash.
func consumeApproval(ctx context.Context, call *auditCall, spec auditSpec) error {
	args := ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}
	broker := ProcessBroker()
	if broker == nil {
		return externalConnectorError(args, ExternalConnectorApprovalPending,
			redact.Guidance("approval %s is held by the daemon, which is not running; start it with `cerberus daemon`, then retry", spec.approvalID))
	}
	planHash, err := specPlanHash(ctx, spec)
	if err != nil {
		return externalConnectorError(args, ExternalConnectorPlanStale,
			redact.GuidanceWrap(err, "approval %s cannot be checked against the plan, which could not be computed now, so nothing ran", spec.approvalID))
	}
	a, err := broker.Consume(ctx, spec.approvalID, approval.ConsumeCheck{Connector: spec.connector, Operation: spec.operation, Principal: call.intent.Principal,
		ArgsDigest: call.intent.ArgsDigest, PlanHash: planHash, OperationID: call.intent.OperationID})
	if err != nil {
		return consumeRefusal(args, spec.approvalID, a, err)
	}
	call.intent.ApprovalID, call.intent.PlanHash = a.ID, planHash
	return nil
}

// consumeRefusal codes a consume the broker refused.
func consumeRefusal(args ExternalConnectorOperationArgs, id string, a approval.Approval, err error) error {
	switch {
	case errors.Is(err, approval.ErrExpired):
		return approvalExpiredError(args, a)
	case errors.Is(err, approval.ErrPlanStale):
		return planStaleError(args, a)
	case errors.Is(err, approval.ErrArgsMismatch):
		return externalConnectorError(args, ExternalConnectorPlanStale,
			redact.Guidance("approval %s was for other arguments; resend exactly the arguments that were approved, or retry without the approval id to ask again", a.ID))
	case errors.Is(err, approval.ErrOtherOperation):
		return externalConnectorError(args, ExternalConnectorPlanStale,
			redact.Guidance("approval %s is for %s %s, not this operation; nothing ran", a.ID, a.Connector, a.Operation))
	case errors.Is(err, approval.ErrOtherPrincipal):
		return externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.Guidance("approval %s belongs to another caller (it was asked for by %s over %s); nothing ran. Retry without the approval id to ask for your own", a.ID, a.Principal.Kind, a.Principal.Via))
	case errors.Is(err, approval.ErrNotApproved) && a.Status == approval.Pending:
		return approvalPendingError(args, a)
	case errors.Is(err, approval.ErrNotApproved):
		return externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.Guidance("approval %s is %s and cannot be used; retry without it to ask again", a.ID, a.Status))
	case errors.Is(err, approval.ErrNotFound):
		return externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.Guidance("there is no approval %s; retry without it to ask for one", id))
	case errors.Is(err, approval.ErrNoPresence):
		return externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.Guidance("approval %s needs a presence proof this Cerberus can verify, and has none; nothing ran", a.ID))
	}
	return externalConnectorError(args, ExternalConnectorAuditUnavailable, err)
}

// approvalTerms is how an approve decision is to be met: the strongest
// channel any matching rule asks for, and out of band wherever a TTY
// confirmation is not enough — prod, shared and not-ours targets (Decision
// 3), read strictly when unlabeled.
func approvalTerms(t target.Target, res policy.Result) (channel, scope string, ttl time.Duration) {
	channel, scope, ttl = approval.ChannelTTYConfirm, approval.ScopeOnce, DefaultApprovalTTL
	for _, m := range res.Matched {
		if m.Decision != policy.Approve || m.Approval == nil {
			continue
		}
		if m.Approval.Channel == approval.ChannelOutOfBand {
			channel = approval.ChannelOutOfBand
		}
		if m.Approval.Scope != "" {
			scope = m.Approval.Scope
		}
		if m.Approval.TTL > 0 {
			ttl = m.Approval.TTL
		}
	}
	protected := t.Env == target.EnvProd || t.Env == target.EnvUnknown || t.Env == "" ||
		t.AdminFor == target.AdminShared || (t.Owner != target.OwnerSelf)
	if protected {
		channel = approval.ChannelOutOfBand
	}
	return channel, scope, ttl
}

// ApprovalRef is the machine-readable half of approval_pending: which
// approval, until when, and the command that decides it.
type ApprovalRef struct {
	ID          string    `json:"id"`
	ExpiresAt   time.Time `json:"expires_at"`
	ApproveWith string    `json:"approve_with"`
}

// approvalPendingError is approval_pending for a, as Guidance: every part of
// it is Cerberus's own.
func approvalPendingError(args ExternalConnectorOperationArgs, a approval.Approval) error {
	err := externalConnectorError(args, ExternalConnectorApprovalPending,
		redact.Guidance("%s %s needs approval (%s, rule %s): approval %s is pending until %s; decide it with `%s`, then retry with the approval id",
			args.Connector, args.Operation, a.Channel, a.Rule, a.ID, a.ExpiresAt.UTC().Format(time.RFC3339), a.ApproveWith()))
	var coded *ExternalConnectorError
	if errors.As(err, &coded) {
		coded.Approval = &ApprovalRef{ID: a.ID, ExpiresAt: a.ExpiresAt, ApproveWith: a.ApproveWith()}
	}
	return err
}

// approvalExpiredError and planStaleError are what a consume answers (the
// consume path lands with the plan hash, P3-2).
func approvalExpiredError(args ExternalConnectorOperationArgs, a approval.Approval) error {
	return externalConnectorError(args, ExternalConnectorApprovalExpired,
		redact.Guidance("approval %s expired at %s and was not used; ask again by retrying without it", a.ID, a.ExpiresAt.UTC().Format(time.RFC3339)))
}

func planStaleError(args ExternalConnectorOperationArgs, a approval.Approval) error {
	return externalConnectorError(args, ExternalConnectorPlanStale,
		redact.Guidance("approval %s was for another plan: what would run now is not what was approved, so nothing ran; retry without the approval id to see the new plan and ask again", a.ID))
}

func decidingRule(res policy.Result) string {
	for _, m := range res.Matched {
		if m.Decision == res.Decision {
			return m.Rule
		}
	}
	return "none"
}

// ApprovalList is the broker's approvals and what folding its store found
// wrong.
type ApprovalList struct {
	Approvals []approval.Approval `json:"approvals"`
	Problems  []string            `json:"problems,omitempty"`
}
