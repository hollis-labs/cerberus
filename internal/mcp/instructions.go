package mcp

import (
	"fmt"

	gmcp "github.com/hollis-labs/go-mcp/server"

	"github.com/hollis-labs/cerberus/internal/policy"
)

// WithInstructions sets the instructions a client is sent at connect.
func WithInstructions(text string) Option { return gmcp.WithInstructions(text) }

// Instructions is what an MCP client is told at connect: what Cerberus is,
// and the posture its calls are evaluated under (section 13: an agent must
// not be able to mistake a permissive install for a secure one). It is
// fixed for the connection; a posture changed later reaches the client when
// it reconnects.
func Instructions(p policy.PostureSummary) string {
	posture := p.String()
	if p.Global == "" {
		posture = policy.PostureSecure
	}
	meaning := "The secure posture: policy is strict for humans and agents alike, " +
		"and an unlabeled target is read as production."
	if p.Permissive() {
		meaning = "The operator has opted into the permissive posture where it says so: policy relaxes there, " +
			"but every operation is still recorded in the audit log, credentials are still redacted, " +
			"and acknowledged=true is still required where an operation demands it."
	}
	return fmt.Sprintf("Cerberus runs, deploys and inspects the operator's infrastructure. Every call is recorded in an audit log. "+
		"Set acknowledged=true only when the person you work for asked for that operation. "+
		"A call answered approval_pending needs your operator's approval, which you cannot give: tell them what its next_step says, "+
		"wait with cerberus_approval_wait, then retry the same call with approval_id. Posture: %s. %s", posture, meaning)
}
