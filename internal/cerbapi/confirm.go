package cerbapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// tty_confirm on the call (P3-3): a person at their own terminal is shown
// the plan, types the target, and the CLI sends the call again on a confirm
// route with the hash of the plan they saw. The approval is requested (or
// the pending one the first attempt asked for is taken), decided and
// consumed in that one call.
//
// It is the same surface confirming its own call, which the out-of-band
// rule (checkDecider) forbids, so it is a separate rule and never reaches
// DecideAs: only tty_confirm approvals, only a human at the CLI, and only
// for the exact plan. The human/cli label is self-reported (P2-1), so an
// agent driving a pty could claim it: tty_confirm is a floor against an
// agent that follows the rules, not a boundary (CERB-GAP-857,
// CERB-GAP-862). Out of band (P3-4) is the boundary.

// canConfirmOnCall is who may confirm their own call: a human at the CLI,
// or a human in a signed-in console session (P3-3b). The console marks the
// session on its principal only after requireSession has checked the
// cookie, so a web caller without a session id, such as a socket POST that
// only claims to be the console, cannot confirm. Both labels are
// self-reported over the socket (CERB-GAP-862): a floor, not a boundary.
func canConfirmOnCall(p audit.Principal) bool {
	if p.Kind != string(PrincipalHuman) {
		return false
	}
	switch p.Via {
	case ViaCLI:
		return true
	case ViaWeb:
		return p.Session != ""
	}
	return false
}

// ConfirmSurface is the Surface a decision made by confirming on the call
// records.
const ConfirmSurface = "tty_confirm"

// checkConfirmedPlan refuses a call whose plan is not the one confirmed.
func checkConfirmedPlan(ctx context.Context, spec auditSpec) error {
	_, err := confirmedPlan(ctx, spec)
	return err
}

// confirmedPlan recomputes the call's plan and checks it is the one the
// person confirmed, returning its hash.
func confirmedPlan(ctx context.Context, spec auditSpec) (string, error) {
	args := ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}
	planHash, err := specPlanHash(ctx, spec)
	if err != nil {
		return "", externalConnectorError(args, ExternalConnectorPlanStale,
			redact.GuidanceWrap(err, "the plan you confirmed cannot be checked, because it could not be computed now, so nothing ran"))
	}
	if planHash != spec.confirmedPlanHash {
		return "", externalConnectorError(args, ExternalConnectorPlanStale,
			redact.Guidance("the plan changed after you were shown it (you confirmed %s, it is now %s), so nothing ran; run the command again to see the new plan", spec.confirmedPlanHash, planHash))
	}
	return planHash, nil
}

// confirmOnCall is an approve decision met by the caller confirming the
// plan on their own terminal.
func confirmOnCall(ctx context.Context, call *auditCall, spec auditSpec, req policy.Request, res policy.Result) error {
	args := ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}
	channel, scope, ttl := approvalTerms(req.Target, res)
	broker := ProcessBroker()
	if channel != approval.ChannelTTYConfirm {
		// A prod, shared or not-ours target, or a rule that asks for out of
		// band: confirming on the call is not enough. Ask as usual.
		if spec.approvalID != "" && broker != nil {
			if a, ok := broker.Get(spec.approvalID); ok && a.Status == approval.Pending {
				return approvalPendingError(args, a)
			}
		}
		return requestApproval(ctx, call, spec, req, res)
	}
	p := call.intent.Principal
	if !canConfirmOnCall(p) {
		return externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.Guidance("confirming on the call is for a person at their own terminal or a signed-in console session, and this caller is %s over %s; the approval has to be decided with `cerberus approvals approve` instead", p.Kind, p.Via))
	}
	planHash, err := confirmedPlan(ctx, spec)
	if err != nil {
		return err
	}
	if broker == nil {
		if spec.approvalID != "" {
			return externalConnectorError(args, ExternalConnectorApprovalRequired,
				redact.Guidance("approval %s is held by the daemon, which is not running; start it with `cerberus daemon`, or confirm again without it", spec.approvalID))
		}
		return confirmInProcess(call, spec, res, planHash)
	}
	var a approval.Approval
	if spec.approvalID != "" {
		got, ok := broker.Get(spec.approvalID)
		switch {
		case !ok:
			return externalConnectorError(args, ExternalConnectorApprovalRequired,
				redact.Guidance("there is no approval %s to confirm; run the command again to ask for one", spec.approvalID))
		case got.Channel != approval.ChannelTTYConfirm:
			return externalConnectorError(args, ExternalConnectorApprovalRequired,
				redact.Guidance("approval %s must be met %s and cannot be confirmed on the call; decide it with `%s`", got.ID, got.Channel, got.ApproveWith()))
		case got.Status != approval.Pending:
			return externalConnectorError(args, ExternalConnectorApprovalRequired,
				redact.Guidance("approval %s is %s, not pending, so there is nothing to confirm; run the command again to ask again", got.ID, got.Status))
		case !approval.SameRequester(got.Principal, p):
			return externalConnectorError(args, ExternalConnectorApprovalRequired,
				redact.Guidance("approval %s was asked for by another caller (%s over %s), so you cannot confirm it on your call; run the command again to ask for your own", got.ID, got.Principal.Kind, got.Principal.Via))
		}
		a = got
	} else {
		if a, err = broker.Request(ctx, call.intent, res, channel, scope, ttl, planHash); err != nil {
			return externalConnectorError(args, ExternalConnectorAuditUnavailable, err)
		}
	}
	if _, err := broker.Decide(ctx, a.ID, approval.Decision{Approve: true, By: p, Surface: ConfirmSurface, Reason: "confirmed on the requesting terminal"}); err != nil {
		return consumeRefusal(args, a.ID, a, err)
	}
	spec.approvalID = a.ID
	return consumeApproval(ctx, call, spec)
}

// confirmInProcess is confirming on the call with no daemon (D3): nothing
// holds the approval, so it is recorded, not stored, under an id of its own:
// requested, decided and consumed.
func confirmInProcess(call *auditCall, spec auditSpec, res policy.Result, planHash string) error {
	var raw [6]byte
	_, _ = rand.Read(raw[:])
	by := call.intent.Principal
	ref := &audit.ApprovalRef{ID: "apr_" + hex.EncodeToString(raw[:]), Channel: approval.ChannelTTYConfirm, Scope: approval.ScopeOnce,
		Rule: decidingRule(res), PlanHash: planHash, ExpiresAt: time.Now().UTC()}
	for _, step := range []struct {
		kind, status string
	}{{audit.KindApprovalRequested, string(approval.Pending)}, {audit.KindApprovalDecided, string(approval.Approved)}, {audit.KindApprovalConsumed, string(approval.Consumed)}} {
		r := *ref
		r.Status = step.status
		if step.kind != audit.KindApprovalRequested {
			r.DecidedBy = &by
		}
		rec := audit.Record{Kind: step.kind, OperationID: call.intent.OperationID, Principal: by, Connector: spec.connector,
			Operation: spec.operation, Effect: call.intent.Effect, Target: call.intent.Target, ArgsDigest: call.intent.ArgsDigest, Approval: &r, Posture: audit.PostureSecure}
		if _, err := call.sink.Write(rec); err != nil {
			return externalConnectorError(ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}, ExternalConnectorAuditUnavailable, err)
		}
	}
	call.intent.ApprovalID, call.intent.PlanHash = ref.ID, planHash
	return nil
}
