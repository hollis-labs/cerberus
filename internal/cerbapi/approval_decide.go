package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// ApprovalDecisionArgs is a decision on a pending approval, as a surface
// sends it: approve or deny, a reason, and for an out-of-band approve the
// presence assertion (P3-4b) that is the only thing that makes it count.
type ApprovalDecisionArgs struct {
	Approve   bool            `json:"approve"`
	Reason    string          `json:"reason,omitempty"`
	Assertion json.RawMessage `json:"assertion,omitempty"`
}

// ApprovalRevokeArgs withdraws an approved approval before it is used.
type ApprovalRevokeArgs struct {
	Reason string `json:"reason,omitempty"`
}

// The refusals of a decision, as Guidance: every word is Cerberus's own.
var (
	errMCPNeverApproves = redact.Guidance("an MCP client never approves: the one who asks cannot approve (I5). Approve it on a terminal with `cerberus approvals approve <id>`, or on the console's approvals page")
	errApproverNotHuman = redact.Guidance("only a human approves, and this caller is classified as an agent; approve it from an interactive terminal or the console")
	errSelfApproval     = redact.Guidance("the approval must come from a different surface than the request (I5): it was asked for through this one. Approve it on a terminal or on the console, whichever the request did not come from")
	errUnknownSurface   = redact.Guidance("this caller's surface is unknown, so it cannot be told apart from the requester's; approve it from an interactive terminal or the console")
)

// checkDecider is the no-self-approval rule (section 5, I5): an approve comes
// from a human, on a surface other than the one the request came through,
// and never from an MCP client. A deny is always allowed — refusing is
// never the dangerous direction. For an out-of-band approval these labels
// are not enough on their own: they are self-reported, so the approval also
// has to carry a presence assertion the broker verifies (P3-4b), whatever
// the kind label says.
func checkDecider(a approval.Approval, d approval.Decision) error {
	if !d.Approve {
		return nil
	}
	via := d.By.Via
	switch {
	case strings.HasPrefix(via, "mcp"):
		return errMCPNeverApproves
	case via == "" || via == ViaUnknown:
		return errUnknownSurface
	case d.By.Kind != string(PrincipalHuman):
		return errApproverNotHuman
	case via == a.Principal.Via:
		return errSelfApproval
	}
	return nil
}

// DecideAs records a decision made by the caller behind ctx: its principal
// is the decider, and the no-self-approval rule is checked before the
// store's own checks (pending, not expired, and for out of band a verified
// presence assertion).
func (b *Broker) DecideAs(ctx context.Context, id string, args ApprovalDecisionArgs) (approval.Approval, error) {
	a, ok := b.store.Get(id)
	if !ok {
		return approval.Approval{}, approval.ErrNotFound
	}
	d := approval.Decision{Approve: args.Approve, By: principalFor(ctx, auditSpec{}), Reason: args.Reason, Assertion: args.Assertion}
	d.Surface = d.By.Via
	if err := checkDecider(a, d); err != nil {
		return a, err
	}
	return b.Decide(ctx, id, d)
}

// RevokeAs withdraws an approved approval for the caller behind ctx. Anyone
// may revoke: withdrawing an approval is never the dangerous direction.
func (b *Broker) RevokeAs(ctx context.Context, id string, args ApprovalRevokeArgs) (approval.Approval, error) {
	by := approval.Decision{By: principalFor(ctx, auditSpec{}), Reason: args.Reason}
	by.Surface = by.By.Via
	return b.Revoke(ctx, id, by)
}

// approvalErrorStatus maps a decision's refusal to the status a surface
// answers with, and its text, which says what to do.
func approvalErrorStatus(err error, id string) (int, string) {
	switch {
	case errors.Is(err, approval.ErrNotFound):
		return http.StatusNotFound, redact.Guidance("no approval %q; `cerberus approvals list` shows them", id).Error()
	case errors.Is(err, approval.ErrExpired):
		return http.StatusConflict, redact.Guidance("approval %s has expired; retry the operation to ask again", id).Error()
	case errors.Is(err, approval.ErrNotPending):
		return http.StatusConflict, redact.Guidance("approval %s is no longer pending; `cerberus approvals show %s` shows what happened to it", id, id).Error()
	case errors.Is(err, approval.ErrNotApproved):
		return http.StatusConflict, redact.Guidance("approval %s is not approved, so there is nothing to revoke", id).Error()
	case errors.Is(err, approval.ErrNoPresence):
		return http.StatusForbidden, redact.Guidance("approval %s must be approved out of band, with a passkey enrolled for this Cerberus: approve it on the console's approvals page (`cerberus approvals approve %s` opens it), after `cerberus approvals enroll`", id, id).Error()
	case errors.Is(err, errMCPNeverApproves), errors.Is(err, errApproverNotHuman), errors.Is(err, errSelfApproval), errors.Is(err, errUnknownSurface):
		return http.StatusForbidden, err.Error()
	}
	return http.StatusInternalServerError, redact.Text(err.Error())
}
