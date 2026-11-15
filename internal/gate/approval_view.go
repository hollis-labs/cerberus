package gate

import (
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
)

// ApprovalView is an approval as an agent waiting on it sees it (P3-6): its
// state, what it is for, and who decided it. It is an explicit mapping
// because it exists to exclude (ADR-0003): the decision's presence proof
// (the key fingerprint, which now carries the owner-session proof), the args digest and the plan
// hash are the broker's, and an agent waiting for a yes or no needs none of
// them.
type ApprovalView struct {
	ID          string          `json:"id"`
	Status      approval.Status `json:"status"`
	Connector   string          `json:"connector"`
	Operation   string          `json:"operation"`
	Target      string          `json:"target"`
	Channel     string          `json:"channel"`
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
	ApproveWith string          `json:"approve_with,omitempty"`
	Decision    *DecisionView   `json:"decision,omitempty"`
}

// DecisionView is who decided, as a kind and a surface, when, and why.
type DecisionView struct {
	Approve bool      `json:"approve"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason,omitempty"`
}

// ViewOfApproval maps an approval to what an agent is shown.
func ViewOfApproval(a approval.Approval) ApprovalView {
	target := a.Target.Resource
	if target == "" {
		target = a.Target.Kind
	}
	v := ApprovalView{ID: a.ID, Status: a.Status, Connector: a.Connector, Operation: a.Operation, Target: target,
		Channel: a.Channel, CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt}
	if a.Status == approval.Pending {
		v.ApproveWith = ApproveWith(a)
	}
	if d := a.Decision; d != nil {
		by := d.By.Kind
		if d.By.Via != "" {
			by += " via " + d.By.Via
		}
		v.Decision = &DecisionView{Approve: d.Approve, By: by, At: d.At, Reason: d.Reason}
	}
	return v
}
