package cerbapi

import (
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
)

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
