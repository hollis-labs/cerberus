// Package gate is Cerberus's operation gate, extracted from the old build's
// internal/cerbapi: policy loading and gating, approvals, audit records, the
// brakes and egress, as close to verbatim as the extraction allowed.
//
// What changed from the old build is the wiring, not the behavior: the
// process globals (SetPolicyDecisionPoint, SetEnforcement, SetBroker,
// SetBrakes, SetRateLimiter, SetNotifier) are fields of an explicit Gate
// built by New, and every gated lane calls Gate.Begin. Passkey approvals are
// gone (decision 0.1): an out-of-band decision is verified by the owner's
// signed-in console session instead (OwnerSessionVerifier).
package gate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Options are what a Gate is built from. Sink is required; everything else
// has the old build's default for "nothing installed".
type Options struct {
	// Sink is where every intent, outcome and transition is recorded.
	Sink audit.Sink
	// Policy is the decision point every gated operation is authorized
	// against. Nil is the built-in baseline. The daemon's is a
	// policy.Reloading, which follows `cerberus policy apply` itself.
	Policy policy.PDP
	// Enforcement says which operations policy is enforced for. Nil
	// enforces nothing but policy.BaselineEnforced: shadow mode.
	// SnapshotEnforcement{} enforces what the applied snapshot says.
	Enforcement Enforcement
	// Broker holds approvals. Nil is the in-process CLI without a daemon:
	// a tty_confirm is met on the call, and an out-of-band approval cannot
	// be asked for (D3).
	Broker *Broker
	// Brakes are the lockdown, freezes and suspensions (§12). Nil brakes
	// nothing.
	Brakes *Brakes
	// Rates counts rate-limited rules (P5-b). Nil counts and enforces no
	// rate.
	Rates *RateLimiter
	// Logger reports what the gate cannot record. Nil is slog.Default().
	Logger *slog.Logger
	// Now is the gate's clock (the breaker counts on it). Nil is time.Now.
	Now func() time.Time
	// Notify tells the operator something they should see now (the
	// daemon's desktop notification). Nil tells nobody.
	Notify func(title, message string)
	// InProcessStateCheck says whether an in-process caller reads
	// Cerberus's real state (M11). Nil checks nothing.
	InProcessStateCheck func() error
}

// Gate is the operation gate. Build it with New; it is safe for concurrent
// use.
type Gate struct {
	sink         audit.Sink
	pdp          policy.PDP
	enforcement  Enforcement
	broker       *Broker
	brakes       *Brakes
	rates        *RateLimiter
	logger       *slog.Logger
	now          func() time.Time
	notifyFn     func(title, message string)
	inProcessChk func() error
	denials      breakerCounts
}

// New builds a gate from opts. It panics without a sink: there is no silent
// no-op audit.
func New(opts Options) *Gate {
	if opts.Sink == nil {
		panic("gate: New requires an audit sink")
	}
	g := &Gate{sink: opts.Sink, pdp: opts.Policy, enforcement: opts.Enforcement, broker: opts.Broker, brakes: opts.Brakes,
		rates: opts.Rates, logger: opts.Logger, now: opts.Now, notifyFn: opts.Notify, inProcessChk: opts.InProcessStateCheck}
	if g.pdp == nil {
		g.pdp = policy.BaselineOnly(policy.SnapshotBaseline)
	}
	if g.logger == nil {
		g.logger = slog.Default()
	}
	if g.now == nil {
		g.now = time.Now
	}
	if se, ok := g.enforcement.(SnapshotEnforcement); ok && se.PDP == nil {
		g.enforcement = SnapshotEnforcement{PDP: g.pdp}
	}
	if g.rates != nil && g.rates.PDP == nil {
		g.rates.PDP = g.pdp
	}
	return g
}

// Broker is the gate's approval broker, or nil.
func (g *Gate) Broker() *Broker { return g.broker }

// Brakes are the gate's brakes, or nil.
func (g *Gate) Brakes() *Brakes { return g.brakes }

// Policy is the gate's decision point.
func (g *Gate) Policy() policy.PDP { return g.pdp }

// Sink is the gate's audit sink.
func (g *Gate) Sink() audit.Sink { return g.sink }

// Notify tells the operator something they should see now, where the gate
// has a notifier.
func (g *Gate) Notify(title, message string) { g.notify(title, message) }

func (g *Gate) notify(title, message string) {
	if g.notifyFn != nil {
		g.notifyFn(title, message)
	}
}

// Enforcement says which operations policy is enforced for. Until one is
// given, policy stays in shadow: recorded, never enforced, except for the
// built-in protections policy.BaselineEnforced names.
type Enforcement interface {
	Enforced(policy.Request) bool
}

// SnapshotEnforcement enforces what the applied policy snapshot's
// enforcement section says (P3-7): its scoped entries, or everything under
// mode enforce, and for a snapshot that fails its hash check, the last
// verified enforcement. Everything else stays shadow. A zero value given to
// New reads the gate's own decision point.
type SnapshotEnforcement struct {
	PDP policy.PDP
}

// Enforced implements Enforcement. Changing policy itself (`policy apply`,
// `policy enforce`, `posture set`) is never enforced: it has its own gate,
// a terminal and a typed confirmation in the operator's own process, where
// no broker could hold an approval, so enforcing it would let enforcement
// lock the way back to shadow.
func (s SnapshotEnforcement) Enforced(req policy.Request) bool {
	if req.Connector == "policy" {
		return false
	}
	if e, ok := s.PDP.(interface {
		Enforced(policy.Request) (bool, string)
	}); ok {
		on, _ := e.Enforced(req)
		return on
	}
	return false
}

// enforced reports whether policy's decision on req is applied. The built-in
// protections policy.BaselineEnforced names are applied whatever policy
// decision point or enforcement is installed (B2); changing policy itself
// never is (SnapshotEnforcement).
func (g *Gate) enforced(req policy.Request) bool {
	if req.Connector != "policy" && policy.BaselineEnforced(req) {
		return true
	}
	return g.enforcement != nil && g.enforcement.Enforced(req)
}

// Begin is where a gated operation starts (the old build's beginGated): its
// intent record and shadow decision, then, where enforcement is on for it,
// the decision enforced. Every error it returns is already coded for the
// caller (*Error). A refusal is recorded as the call's outcome before it
// returns. Where enforcement is off, this is the intent record and nothing
// more: shadow mode is unchanged.
//
// On success the caller runs the operation, shapes its result with
// Call.Shape (egress), and finishes with Call.Finish. A PlanOnly spec is
// answered with Call.ShowPlan instead of running anything.
func (g *Gate) Begin(ctx context.Context, spec Spec) (*Call, error) {
	args := opArgs{Connector: spec.Connector, Operation: spec.Operation}
	if spec.PlanOnly {
		// A plan request runs nothing but the preview: it is recorded as a
		// dry run, and spends no approval, confirmation or break glass (the
		// old build's admin lane did this before it began the call).
		spec.DryRun, spec.ApprovalID, spec.ConfirmedPlanHash, spec.BreakGlass = true, "", "", nil
	}
	// Before anything else: an in-process mutation reading state other
	// than the real one is refused, not recorded in the wrong log (M11).
	if refusal := g.inProcessStateRefusal(ctx, spec); refusal != nil {
		return nil, refusal
	}
	if spec.Automation {
		call, err := g.beginAudit(ctx, spec)
		if err != nil {
			return nil, codedError(args, CodeAuditUnavailable, err)
		}
		return call, nil
	}
	_, resolved := auditTarget(spec)
	req := policyRequest(ctx, spec, resolved)
	if spec.BreakGlass != nil && !spec.Automation {
		// The break-glass record comes before the call's intent (P3-5b), so
		// the log says someone broke glass even if nothing after it is
		// written. It is written only where there is an approve to get past.
		if g.enforced(req) && g.pdp.Authorize(req).Decision == policy.Approve {
			spec.OperationID = audit.NewID()
			if err := g.recordBreakGlass(ctx, spec, resolved); err != nil {
				return nil, codedError(args, CodeAuditUnavailable, err)
			}
		}
	}
	call, err := g.beginAudit(ctx, spec)
	if err != nil {
		return nil, codedError(args, CodeAuditUnavailable, err)
	}
	// A verified token's scopes (WP-S8) come first: they are what the
	// operator granted the token, in every mode.
	if refusal := scopeRefusal(ctx, spec); refusal != nil {
		call.Finish(refusal)
		return nil, refusal
	}
	// The brakes (§12) come before policy, in every enforcement mode.
	if refusal := g.brakeRefusal(ctx, spec, resolved, spec.DryRun); refusal != nil {
		call.Finish(refusal)
		return nil, refusal
	}
	if !g.enforced(req) {
		// A confirmed plan is checked whether or not policy asked for
		// one: whoever confirmed it was shown it (I6).
		if spec.ConfirmedPlanHash != "" {
			if refusal := checkConfirmedPlan(ctx, spec); refusal != nil {
				call.Finish(refusal)
				return nil, refusal
			}
		}
		call.holdRates(ctx, req)
		return call, nil
	}
	if refusal := g.enforceDecision(ctx, call, spec, req); refusal != nil {
		call.Finish(refusal)
		return nil, refusal
	}
	call.holdRates(ctx, req)
	return call, nil
}

// holdRates counts the call against its matched rates (P5-b) as it is
// about to run; Finish gives the holds back if it is then refused.
func (c *Call) holdRates(ctx context.Context, req policy.Request) {
	l := c.g.rates
	if l == nil || (req.DryRun && !c.spec.pluginPreview()) {
		return
	}
	if holds := rateHolds(ctx, c.spec, c.g.pdp.Authorize(req)); len(holds) > 0 {
		c.rates, c.limiter = l.hold(holds), l
	}
}

// enforceDecision is the enforced decision for a call: nil to let it run, or
// the coded refusal. An approve decision is met by break glass, a confirmed
// plan, an approval named by id, a grant, or else asks the broker for an
// approval and answers approval_pending.
func (g *Gate) enforceDecision(ctx context.Context, call *Call, spec Spec, req policy.Request) error {
	res := g.authorizeRated(ctx, spec, req)
	args := opArgs{Connector: spec.Connector, Operation: spec.Operation}
	switch res.Decision {
	case policy.Allow:
		if spec.ConfirmedPlanHash != "" {
			return checkConfirmedPlan(ctx, spec)
		}
		// A policy that relaxed to allow since the approval was given still
		// spends it, so the record shows it was used (D8).
		if spec.ApprovalID != "" {
			return g.consumeApproval(ctx, call, spec, false)
		}
		return nil
	case policy.DryRunOnly:
		if req.DryRun {
			return nil
		}
		return codedError(args, CodePolicyDenied,
			redact.Guidance("policy allows only a dry run of %s %s (rule %s); run it with --dry-run", spec.Connector, spec.Operation, decidingRule(res)))
	case policy.Approve:
		if req.DryRun {
			return nil
		}
		if spec.BreakGlass != nil {
			return g.breakGlassOnCall(ctx, call, spec, req, res)
		}
		if spec.ConfirmedPlanHash != "" {
			return g.confirmOnCall(ctx, call, spec, req, res)
		}
		if spec.OwnerAuthorized {
			// The owner's approval, which the lane asks for next (Call.OwnerApproval), meets
			// this approve too: one approval, not a second one beside it.
			call.policyApprove = &res
			return nil
		}
		if spec.ApprovalID != "" {
			// The channel policy asks for now, not the one the store says
			// the approval was met on (H4).
			channel, _, _ := approvalTerms(req.Target, res)
			return g.consumeApproval(ctx, call, spec, channel == approval.ChannelOutOfBand)
		}
		if used, err := g.useGrant(ctx, call, spec, req, res); used || err != nil {
			return err
		}
		return g.requestApproval(ctx, call, spec, req, res)
	case policy.Deny:
	}
	reason := res.Reason()
	if reason == "" {
		reason = "no rule allows it"
	}
	if spec.BreakGlass != nil {
		return codedError(args, CodePolicyDenied,
			redact.Guidance("policy denies %s %s: %s (rule %s); break glass gets past an approve, never a deny, so nothing ran",
				spec.Connector, spec.Operation, reason, decidingRule(res)))
	}
	return codedError(args, CodePolicyDenied,
		redact.Guidance("policy denies %s %s: %s (rule %s); `cerberus policy explain %s.%s` shows every rule that matched",
			spec.Connector, spec.Operation, reason, decidingRule(res), spec.Connector, spec.Operation))
}

// requestApproval asks the gate's broker for an approval and answers
// approval_pending with how to decide it. With no broker — the in-process
// CLI without a daemon — an approval that has to come out of band cannot be
// asked for at all, so the answer names how to start the daemon (D3).
func (g *Gate) requestApproval(ctx context.Context, call *Call, spec Spec, req policy.Request, res policy.Result) error {
	args := opArgs{Connector: spec.Connector, Operation: spec.Operation}
	channel, scope, ttl := approvalTerms(req.Target, res)
	broker := g.broker
	if broker == nil {
		if channel == approval.ChannelTTYConfirm {
			err := codedError(args, CodeApprovalRequired,
				redact.Guidance("%s %s needs your confirmation (rule %s): run it from an interactive terminal, where Cerberus shows the plan and asks you to type the target", spec.Connector, spec.Operation, decidingRule(res)))
			var coded *Error
			if errors.As(err, &coded) {
				// No id: without a daemon nothing holds it. The channel is
				// what tells the CLI it can confirm on the call itself.
				coded.Approval = &ApprovalRef{Channel: approval.ChannelTTYConfirm}
			}
			return err
		}
		return codedError(args, CodeApprovalPending,
			redact.Guidance("%s %s needs an out-of-band approval (rule %s), and only the daemon can hold one; start it with `cerberus daemon`, then retry", spec.Connector, spec.Operation, decidingRule(res)))
	}
	snap, err := specPlanSnapshot(ctx, spec)
	if err != nil {
		return codedError(args, CodeApprovalRequired,
			redact.GuidanceWrap(err, "%s %s needs approval, and approval binds to a plan, which could not be computed", spec.Connector, spec.Operation))
	}
	a, err := broker.Request(ctx, call.intent, res, channel, scope, ttl, snap)
	if err != nil {
		return codedError(args, CodeAuditUnavailable, err)
	}
	return approvalPendingError(args, a)
}

// specPlanHash is the lane's plan for the call, hashed. A lane without a
// plan function binds its approvals to the arguments alone.
func specPlanHash(ctx context.Context, spec Spec) (string, error) {
	if spec.Plan == nil {
		return "", nil
	}
	p, err := spec.Plan(ctx)
	if err != nil {
		return "", err
	}
	return p.Hash()
}

// useGrant runs the call under an active grant that covers it (P3-5), and
// reports whether one did. A grant is looked for only when policy as it
// reads now still asks for a grant (session or window): a rule narrowed
// to once, or to out of band where the grant was met on a terminal, is
// not covered by the grant it gave before. It never widens a deny: only
// the approve branch calls it.
func (g *Gate) useGrant(ctx context.Context, call *Call, spec Spec, req policy.Request, res policy.Result) (bool, error) {
	broker := g.broker
	if broker == nil {
		return false, nil
	}
	channel, scope, _ := approvalTerms(req.Target, res)
	if scope != approval.ScopeSession && scope != approval.ScopeWindow {
		return false, nil
	}
	a, err := broker.UseGrant(ctx, approval.GrantCheck{Connector: spec.Connector, Operation: spec.Operation, Target: call.intent.Target,
		Principal: call.intent.Principal, OperationID: call.intent.OperationID, RequireOutOfBand: channel == approval.ChannelOutOfBand})
	switch {
	case errors.Is(err, approval.ErrNotFound):
		return false, nil
	case err != nil:
		return true, consumeRefusal(opArgs{Connector: spec.Connector, Operation: spec.Operation}, a.ID, a, err)
	}
	call.intent.ApprovalID = a.ID
	return true, nil
}

// consumeApproval spends the call's approval, write-ahead, after
// recomputing its plan the same way it was computed when the approval was
// asked for (I6). What the call runs under is written on its records: the
// outcome carries approval_id and plan_hash.
func (g *Gate) consumeApproval(ctx context.Context, call *Call, spec Spec, requireOutOfBand bool) error {
	args := opArgs{Connector: spec.Connector, Operation: spec.Operation}
	broker := g.broker
	if broker == nil {
		return codedError(args, CodeApprovalPending,
			redact.Guidance("approval %s is held by the daemon, which is not running; start it with `cerberus daemon`, then retry", spec.ApprovalID))
	}
	planHash, err := specPlanHash(ctx, spec)
	if err != nil {
		return codedError(args, CodePlanStale,
			redact.GuidanceWrap(err, "approval %s cannot be checked against the plan, which could not be computed now, so nothing ran", spec.ApprovalID))
	}
	a, err := broker.Consume(ctx, spec.ApprovalID, approval.ConsumeCheck{Connector: spec.Connector, Operation: spec.Operation, Principal: call.intent.Principal, Target: call.intent.Target,
		ArgsDigest: call.intent.ArgsDigest, PlanHash: planHash, OperationID: call.intent.OperationID, RequireOutOfBand: requireOutOfBand})
	if err != nil {
		return consumeRefusal(args, spec.ApprovalID, a, err)
	}
	call.intent.ApprovalID, call.intent.PlanHash = a.ID, planHash
	return nil
}

// consumeRefusal codes a consume the broker refused.
func consumeRefusal(args opArgs, id string, a approval.Approval, err error) error {
	switch {
	case errors.Is(err, approval.ErrExpired):
		return approvalExpiredError(args, a)
	case errors.Is(err, approval.ErrPlanStale):
		return planStaleError(args, a)
	case errors.Is(err, approval.ErrArgsMismatch):
		return codedError(args, CodePlanStale,
			redact.Guidance("approval %s was for other arguments; resend exactly the arguments that were approved, or retry without the approval id to ask again", a.ID))
	case errors.Is(err, approval.ErrOtherTarget):
		return codedError(args, CodePlanStale,
			redact.Guidance("approval %s is a grant for another target; nothing ran. Retry without the approval id to ask for this one", a.ID))
	case errors.Is(err, approval.ErrOtherOperation):
		return codedError(args, CodePlanStale,
			redact.Guidance("approval %s is for %s %s, not this operation; nothing ran", a.ID, a.Connector, a.Operation))
	case errors.Is(err, approval.ErrWeakerChannel):
		return codedError(args, CodeApprovalRequired,
			redact.Guidance("approval %s was met on %s, and this call now needs out-of-band approval by the owner on the console; nothing ran. Retry without the approval id to ask for one", a.ID, a.Channel))
	case errors.Is(err, approval.ErrOtherPrincipal):
		return codedError(args, CodeApprovalRequired,
			redact.Guidance("approval %s belongs to another caller (it was asked for by %s over %s); nothing ran. Retry without the approval id to ask for your own", a.ID, a.Principal.Kind, a.Principal.Via))
	case errors.Is(err, approval.ErrNotApproved) && a.Status == approval.Pending:
		return approvalPendingError(args, a)
	case errors.Is(err, approval.ErrNotApproved):
		return codedError(args, CodeApprovalRequired,
			redact.Guidance("approval %s is %s and cannot be used; retry without it to ask again", a.ID, a.Status))
	case errors.Is(err, approval.ErrNotFound):
		return codedError(args, CodeApprovalRequired,
			redact.Guidance("there is no approval %s; retry without it to ask for one", id))
	case errors.Is(err, approval.ErrNoPresence):
		return codedError(args, CodeApprovalRequired,
			redact.Guidance("approval %s was not approved by the owner signed in to the console, so it carries no verification this Cerberus accepts; nothing ran", a.ID))
	}
	return codedError(args, CodeAuditUnavailable, err)
}

// DefaultApprovalTTL is how long a request waits for a decision, and an
// approval for its use, when the rule does not say.
const DefaultApprovalTTL = time.Hour

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
	if protectedTarget(t) {
		channel = approval.ChannelOutOfBand
	}
	return channel, scope, ttl
}

// ApprovalChannel is how an approve decision on t would have to be met
// (tty_confirm or out_of_band), by the rule that asked, the target's labels
// (Decision 3), and how many approvers the rule asks for. It is what the
// gate uses when it asks the broker, exported for `cerberus policy report`.
func ApprovalChannel(t target.Target, res policy.Result) (channel string, approvers int) {
	channel, _, _ = approvalTerms(t, res)
	for _, m := range res.Matched {
		if m.Decision == policy.Approve && m.Approval != nil && m.Approval.Approvers > approvers {
			approvers = m.Approval.Approvers
		}
	}
	return channel, approvers
}

// protectedTarget is a prod, shared or not-ours target, or one whose labels
// are unknown (Decision 3): out of band, and a grant on it is loud.
func protectedTarget(t target.Target) bool {
	return t.Env == target.EnvProd || t.Env == target.EnvUnknown || t.Env == "" ||
		t.AdminFor == target.AdminShared || (t.Owner != target.OwnerSelf)
}

// ProtectedTarget reports a recorded target that is prod, shared, not ours
// or unlabeled: where a grant is loud.
func ProtectedTarget(t audit.Target) bool { return protectedTarget(auditTargetLabels(t)) }

// auditTargetLabels is a recorded target's labels, for protectedTarget.
func auditTargetLabels(t audit.Target) target.Target {
	return target.Target{Labels: target.Labels{Env: target.Env(t.Env), Owner: t.Owner}, AdminFor: t.Admin}
}

// ApprovalRef is the machine-readable half of approval_pending: which
// approval, until when, and where it is decided.
type ApprovalRef struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
	// ApproveWith is where the approval is decided (ApproveWith): the
	// console's approvals page. Clients display it and never parse it.
	ApproveWith string `json:"approve_with"`
	// Channel is how the approval is met. tty_confirm tells a CLI on a
	// terminal it can show the plan and confirm on the call (P3-3).
	Channel string `json:"channel,omitempty"`
}

// approvalPendingError is approval_pending for a, as Guidance: every part of
// it is Cerberus's own.
func approvalPendingError(args opArgs, a approval.Approval) error {
	grant := ""
	if a.IsGrant() {
		// Once approved, a grant covers the same operation on this target by
		// this caller for its TTL, with no approval id needed.
		grant = fmt.Sprintf("; it is a %s grant, good for %s once approved", a.Scope, a.TTL)
	}
	err := codedError(args, CodeApprovalPending,
		redact.Guidance("%s %s needs approval (%s, rule %s): approval %s is pending until %s%s; %s, signed in as the owner, then retry with the approval id",
			args.Connector, args.Operation, a.Channel, a.Rule, a.ID, a.ExpiresAt.UTC().Format(time.RFC3339), grant, ApproveWith(a)))
	var coded *Error
	if errors.As(err, &coded) {
		coded.Approval = approvalReference(a)
	}
	return err
}

// approvalExpiredError and planStaleError are what a consume answers.
func approvalExpiredError(args opArgs, a approval.Approval) error {
	return codedError(args, CodeApprovalExpired,
		redact.Guidance("approval %s expired at %s and was not used; ask again by retrying without it", a.ID, a.ExpiresAt.UTC().Format(time.RFC3339)))
}

func planStaleError(args opArgs, a approval.Approval) error {
	return codedError(args, CodePlanStale,
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

// approvalReference is the ApprovalRef of a. The old build added a
// passkey-setup hint for an out-of-band approval; with passkeys gone
// (decision 0.1) there is nothing to set up.
func approvalReference(a approval.Approval) *ApprovalRef {
	return &ApprovalRef{ID: a.ID, ExpiresAt: a.ExpiresAt, ApproveWith: ApproveWith(a), Channel: a.Channel}
}

// ApproveWith is where a is decided, for a caller told to wait for it: the owner signed in to
// the console, on its approvals page (decision 0.1). The old build's value,
// approval.Approval.ApproveWith, named `cerberus approvals approve <id>`, a verb this build does
// not carry; internal/approval is shared with main, so the value is replaced here. Tachyon and
// tether sysop display approve_with (or pass the body through) and do not parse it.
func ApproveWith(a approval.Approval) string {
	return "approve it on the console: " + ConsoleApprovalLink(a.ID)
}

// ConsoleApprovalLink is the console's deep link to one approval, the console contract's
// /approvals?id=<id> (docs/substrate-port/console-api-contract.md section 4): the page signs the
// owner in if needed, then opens the approval.
func ConsoleApprovalLink(id string) string {
	return "/approvals?id=" + url.QueryEscape(id)
}

// inProcessStateRefusal refuses an in-process mutation when the gate's
// InProcessStateCheck says this process's state is not the real one: the
// brakes, policy, approvals and audit log live under the account's home,
// and a CLI run with HOME pointed elsewhere would run a mutation under a
// scratch lockdown, a scratch policy and a scratch audit log (M11). Reads,
// dry runs, plans and automation pass; so does everything through the
// daemon, which reads its own state.
func (g *Gate) inProcessStateRefusal(ctx context.Context, spec Spec) error {
	if g.inProcessChk == nil || CallerSurfaceFrom(ctx) != SurfaceInProcess || spec.Automation || spec.DryRun || spec.PlanOnly {
		return nil
	}
	if spec.Known && (spec.Op.Effect == contract.EffectRead || spec.Op.Effect == contract.EffectReadSensitive) {
		return nil
	}
	err := g.inProcessChk()
	if err == nil {
		return nil
	}
	return codedError(opArgs{Connector: spec.Connector, Operation: spec.Operation}, CodeAuditUnavailable,
		redact.GuidanceWrap(err, "%s %s did not run: run in this process, it would be braked, authorized and recorded by state other than Cerberus's real state", spec.Connector, spec.Operation))
}
