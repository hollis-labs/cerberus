package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
)

// Break glass (P3-5b): a person at their own terminal gets past an approve
// decision, whatever channel or number of approvers it asks for, with a
// reason and the target typed. It never gets past a deny. It is loud: a
// break_glass record before the intent, a notification, and a follow-up in
// `cerberus status` until acknowledged, plus the console header for a day.
// It is rate-limited per target by the snapshot's break_glass. On a prod,
// shared, not-ours or unlabeled target it is completed with a passkey on
// the console (D2), so a pty alone is not enough there.

// ConfirmSurface for a break-glass decision.
const breakGlassSurface = "break_glass"

var notifier atomic.Pointer[func(title, message string)]

// SetNotifier installs how the operator is told a break-glass happened: the
// daemon's desktop notification. Nil removes it.
func SetNotifier(notify func(title, message string)) {
	if notify == nil {
		notifier.Store(nil)
		return
	}
	notifier.Store(&notify)
}

func notify(title, message string) {
	if n := notifier.Load(); n != nil {
		(*n)(title, message)
	}
}

// recordBreakGlass writes the break_glass record, before the intent it
// links to.
func recordBreakGlass(ctx context.Context, sink audit.Sink, spec auditSpec, resolved target.Target) error {
	tgt, _ := auditTarget(spec)
	rec := audit.Record{Kind: audit.KindBreakGlass, OperationID: spec.operationID, Principal: principalFor(ctx, spec),
		Connector: spec.connector, Operation: spec.operation, Effect: string(spec.op.Effect), Target: tgt,
		BreakGlass: &audit.BreakGlassRef{Reason: spec.breakGlass.Reason, Protected: protectedTarget(resolved)}, Posture: audit.PostureSecure}
	_, err := sink.Write(rec)
	return err
}

// typedTargetName is what a person types to name a target: the resource,
// else its first identifying field, else its kind.
func typedTargetName(t audit.Target) string {
	if t.Resource != "" {
		return t.Resource
	}
	keys := make([]string, 0, len(t.Fields))
	for k := range t.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return t.Fields[keys[0]]
	}
	return t.Kind
}

// breakGlassOnCall is an approve decision met by breaking glass.
func breakGlassOnCall(ctx context.Context, call *auditCall, spec auditSpec, req policy.Request, res policy.Result) error {
	args := ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}
	refuse := func(format string, a ...any) error {
		return externalConnectorError(args, ExternalConnectorApprovalRequired, redact.Guidance(format, a...))
	}
	broker := ProcessBroker()
	if broker == nil {
		return refuse("break glass needs the daemon, which keeps its record, its rate limit and its follow-up; start it with `cerberus daemon`, then retry")
	}
	p := call.intent.Principal
	if p.Kind != string(PrincipalHuman) || p.Via != ViaCLI {
		return refuse("break glass is for a person at their own terminal, and this caller is %s over %s; nothing ran", p.Kind, p.Via)
	}
	bg := spec.breakGlass
	if strings.TrimSpace(bg.Reason) == "" {
		return refuse("break glass needs a reason (--reason \"…\"), which goes on its record; nothing ran")
	}
	name := typedTargetName(call.intent.Target)
	if bg.Typed != name {
		return refuse("break glass needs the target typed exactly (%s); nothing ran", name)
	}
	if spec.approvalID != "" {
		// The retry after the passkey on a protected target.
		a, ok := broker.Get(spec.approvalID)
		if !ok || a.BreakGlass == nil {
			return refuse("approval %s is not a break-glass request; run the command again with --break-glass to ask for one", spec.approvalID)
		}
		// On a protected target the retry must spend a passkey approval,
		// whatever channel the store says this one was met on (H4).
		if err := consumeApproval(ctx, call, spec, protectedTarget(req.Target)); err != nil {
			return err
		}
		notify("Cerberus: break glass used", fmt.Sprintf("%s.%s on %s: %s", spec.connector, spec.operation, name, bg.Reason))
		return nil
	}
	limits := policy.BreakGlassLimitsOf(PolicyDecisionPoint())
	if uses := broker.BreakGlassSince(call.intent.Target, time.Now().Add(-limits.Window)); len(uses) >= limits.PerTarget {
		next := uses[0].CreatedAt.Add(limits.Window)
		return refuse("break glass on %s is limited to %s, and it has been used %d times; the next is possible at %s. The limit is break_glass in policy, changed with `cerberus policy apply`",
			name, limits, len(uses), next.Local().Format(time.RFC3339))
	}
	snap, err := specPlanSnapshot(ctx, spec)
	if err != nil {
		return externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.GuidanceWrap(err, "break glass binds to a plan, which could not be computed; nothing ran"))
	}
	record := &approval.BreakGlass{Reason: bg.Reason, Typed: bg.Typed}
	_, _, ttl := approvalTerms(req.Target, res)
	if protectedTarget(req.Target) {
		// D2: on a protected target the break glass is completed with a
		// passkey, on the console, so a pty is not enough.
		a, reqErr := broker.request(ctx, call.intent, res, approval.ChannelOutOfBand, approval.ScopeOnce, ttl, snap, record)
		if reqErr != nil {
			return externalConnectorError(args, ExternalConnectorAuditUnavailable, reqErr)
		}
		notify("Cerberus: break glass requested", fmt.Sprintf("%s.%s on %s: %s", spec.connector, spec.operation, name, bg.Reason))
		pending := externalConnectorError(args, ExternalConnectorApprovalPending,
			redact.Guidance("BREAK GLASS on %s, a prod, shared or not-ours target, is completed with your passkey: approve %s on the console (`%s` opens it), then run this again with --break-glass and --approval %s",
				name, a.ID, a.ApproveWith(), a.ID))
		var coded *ExternalConnectorError
		if errors.As(pending, &coded) {
			coded.Approval = &ApprovalRef{ID: a.ID, ExpiresAt: a.ExpiresAt, ApproveWith: a.ApproveWith(), Channel: a.Channel}
		}
		return pending
	}
	a, err := broker.request(ctx, call.intent, res, approval.ChannelBreakGlass, approval.ScopeOnce, ttl, snap, record)
	if err != nil {
		return externalConnectorError(args, ExternalConnectorAuditUnavailable, err)
	}
	if _, err := broker.Decide(ctx, a.ID, approval.Decision{Approve: true, By: p, Surface: breakGlassSurface, Reason: bg.Reason}); err != nil {
		return consumeRefusal(args, a.ID, a, err)
	}
	spec.approvalID = a.ID
	if err := consumeApproval(ctx, call, spec, false); err != nil {
		return err
	}
	notify("Cerberus: break glass used", fmt.Sprintf("%s.%s on %s: %s", spec.connector, spec.operation, name, bg.Reason))
	return nil
}

// BreakGlassSince are the break-glass uses on a target since since.
func (b *Broker) BreakGlassSince(t audit.Target, since time.Time) []approval.Approval {
	return b.store.BreakGlassSince(t, since)
}

// UnackedBreakGlass are the break-glass uses whose follow-up is open.
func (b *Broker) UnackedBreakGlass() []approval.Approval { return b.store.UnackedBreakGlass() }

// AckBreakGlassAs closes a break-glass use's follow-up for the caller behind
// ctx: a person, never an MCP client, recorded as break_glass_acked.
func (b *Broker) AckBreakGlassAs(ctx context.Context, id, note string) (approval.Approval, error) {
	by := principalFor(ctx, auditSpec{})
	if strings.HasPrefix(by.Via, "mcp") || by.Kind != string(PrincipalHuman) {
		return approval.Approval{}, errAckNotHuman
	}
	a, err := b.store.AckBreakGlass(id, approval.Decision{By: by, Reason: note})
	if err != nil {
		return a, err
	}
	rec := audit.Record{Kind: audit.KindBreakGlassAcked, OperationID: a.ConsumedOperationID, Principal: by, Connector: a.Connector,
		Operation: a.Operation, Effect: a.Effect, Target: a.Target, Posture: audit.PostureSecure,
		BreakGlass: &audit.BreakGlassRef{Reason: a.BreakGlass.Reason, Protected: ProtectedTarget(a.Target), ApprovalID: a.ID}}
	if _, werr := b.sink.Write(rec); werr != nil {
		b.logger.Error("audit.write_failed", "kind", rec.Kind, "approval", a.ID, "error", redact.Text(werr.Error()))
	}
	return a, nil
}

var errAckNotHuman = redact.Guidance("a break-glass follow-up is acknowledged by a person, on a terminal: run `cerberus approvals ack-break-glass <id>` yourself")
