package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Approvals over MCP (P3-6). An agent is told approval_pending with what to
// ask its human, waits with cerberus_approval_wait, and retries the same call
// with approval_id. It never decides one: no tool here approves, denies or
// revokes, and the daemon refuses an MCP principal that tries (I5).

const (
	argApprovalID = "approval_id"

	approvalWaitDefault = 30 * time.Second
	approvalWaitMax     = 60 * time.Second
)

// approvalPollInterval is how often the wait tool reads the approval. Tests
// shorten it.
var approvalPollInterval = 500 * time.Millisecond

// withApprovalArg adds approval_id to a tool over an operation that can need
// an approval: anything but a read. It is derived from the contract, as the
// hints are, so a new gated tool advertises it without being told to; its
// handler passes it on explicitly, never through a context, as it does
// acknowledged.
func withApprovalArg(t Tool, op contract.Operation) Tool {
	if op.Effect == contract.EffectRead || ungatedTools[t.Name] != "" {
		return t
	}
	schema, ok := t.InputSchema.(map[string]any)
	if !ok {
		return t
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return t
	}
	props[argApprovalID] = approvalArgSchema()
	return t
}

// ungatedTools are tools over a non-read operation that does not pass the
// gate, so an approval can neither be asked for nor used there, and the
// tool does not offer approval_id. Each says why.
var ungatedTools = map[string]string{
	// local.logs is read_sensitive, but the runtime reads the log file
	// directly and never calls beginGated (CERB-GAP-889).
	"cerberus_resource_logs": "resource logs are read without the gate",
}

func approvalArgSchema() map[string]any {
	return map[string]any{"type": "string", "description": "The approval id from an approval_pending answer, once your operator has approved it. Retry with exactly the same arguments; anything different is plan_stale."}
}

// toolRefusal is the body of a refusal an MCP client receives: the OpResult
// fields a client already reads, the refusal's code, for approval_pending
// the approval to wait for, and the next step in words an agent can act on.
type toolRefusal struct {
	Success  bool                 `json:"success"`
	Error    string               `json:"error"`
	Code     string               `json:"code,omitempty"`
	Approval *cerbapi.ApprovalRef `json:"approval,omitempty"`
	NextStep string               `json:"next_step,omitempty"`
}

// refusalFor is err's refusal body, with msg as its already-rendered text.
// An error with no code is a plain failure.
func refusalFor(err error, msg string) toolRefusal {
	out := toolRefusal{Success: false, Error: msg}
	var coded *cerbapi.ExternalConnectorError
	if !errors.As(err, &coded) || coded.Code == "" {
		return out
	}
	out.Code = string(coded.Code)
	if coded.Approval != nil && coded.Approval.ID != "" {
		ref := *coded.Approval
		out.Approval = &ref
	}
	if next := nextStep(coded.Code, out.Approval); next != nil {
		out.NextStep = next.Error()
	}
	return out
}

// nextStep is what an agent does after a refusal: for an approval, what to
// ask its human and how to come back. Every part of it is Cerberus's own,
// with ids and commands as arguments.
func nextStep(code cerbapi.ExternalConnectorErrorCode, ref *cerbapi.ApprovalRef) error {
	switch code {
	case cerbapi.ExternalConnectorApprovalPending:
		if ref == nil {
			return redact.Guidance("the approval can only be held by the Cerberus daemon; ask your operator to start it (`cerberus daemon`), then retry this call")
		}
		ask := redact.Guidance("ask your operator to run `%s` in their terminal, where they see the plan and confirm it", ref.ApproveWith)
		if ref.Channel == approval.ChannelOutOfBand {
			ask = redact.Guidance("ask your operator to approve %s on the Cerberus console with their passkey (Touch ID or a security key); `%s` in their terminal opens the page", ref.ID, ref.ApproveWith)
		}
		return redact.Guidance("%s. You cannot approve it yourself. Then call cerberus_approval_wait with id %s, and once it is approved retry this call with the same arguments and approval_id %s", ask.Error(), ref.ID, ref.ID)
	case cerbapi.ExternalConnectorApprovalRequired:
		if ref != nil && ref.Channel == approval.ChannelTTYConfirm {
			return redact.Guidance("this needs your operator's confirmation at an interactive terminal, which you cannot give; ask them to run the operation themselves")
		}
		return redact.Guidance("retry this call without approval_id to ask for a new approval")
	case cerbapi.ExternalConnectorApprovalExpired:
		return redact.Guidance("the approval expired unused; retry this call without approval_id to ask for a new one")
	case cerbapi.ExternalConnectorPlanStale:
		return redact.Guidance("what would run changed since it was approved, so nothing ran; retry without approval_id to see the new plan and ask again")
	case cerbapi.ExternalConnectorPolicyDenied:
		return redact.Guidance("policy denies this call and no approval can change that; do not retry, and tell your operator what you were trying to do")
	default:
		// Every other refusal's own message says what to do.
		return nil
	}
}

// isCoded is whether err is a refusal with a code.
func isCoded(err error) bool {
	var c *cerbapi.ExternalConnectorError
	return errors.As(err, &c) && c.Code != ""
}

// approvalReader is the daemon's approvals as the wait tool reads them.
type approvalReader interface {
	GetApproval(ctx context.Context, id string) (approval.Approval, error)
}

// approvalWaitResult is what cerberus_approval_wait answers: the approval
// as it reads now, whether it changed while waiting, and what to do next.
type approvalWaitResult struct {
	Approval cerbapi.ApprovalView `json:"approval"`
	Changed  bool                 `json:"changed"`
	Waited   string               `json:"waited"`
	NextStep string               `json:"next_step"`
}

// NewCerberusApprovalWaitTool creates cerberus_approval_wait: wait up to a
// minute for an approval to change state, then report it. It is a read; it
// never decides anything.
func NewCerberusApprovalWaitTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name: "cerberus_approval_wait",
		Description: "Wait for an approval to be decided: after an approval_pending answer, ask your operator to approve it, then call this with its id. " +
			"Returns when the approval changes state or at the timeout (default 30s, at most 60s), with the next step. It never approves anything.",
		InputSchema: objectSchema(map[string]interface{}{
			"id":              map[string]interface{}{"type": "string", "description": "Approval ID, from approval_pending."},
			"timeout_seconds": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 60, "description": "How long to wait, 1 to 60 seconds. Default 30."},
		}, "id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			reader, ok := client.(approvalReader)
			if !ok {
				return toolResult(lifecycleResult{Success: false, Error: "this MCP server cannot read approvals; it is not connected to the Cerberus daemon"})
			}
			id := stringArg(args, "id")
			if id == "" {
				return toolResult(lifecycleResult{Success: false, Error: "id is required: the approval id from approval_pending"})
			}
			return waitForApproval(ctx, reader, id, waitTimeout(args))
		},
	})
}

func waitTimeout(args map[string]interface{}) time.Duration {
	secs, ok := args["timeout_seconds"].(float64)
	if !ok || secs <= 0 {
		return approvalWaitDefault
	}
	if d := time.Duration(secs * float64(time.Second)); d < approvalWaitMax {
		return d
	}
	return approvalWaitMax
}

func waitForApproval(ctx context.Context, reader approvalReader, id string, timeout time.Duration) (any, error) {
	start := time.Now()
	first, err := reader.GetApproval(ctx, id)
	if err != nil {
		return connectorFailure(ctx, err)
	}
	current := first
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(approvalPollInterval)
	defer tick.Stop()
	for current.Status == first.Status && waiting(current.Status) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return waitResult(current, false, start), nil
		case <-tick.C:
		}
		if current, err = reader.GetApproval(ctx, id); err != nil {
			return connectorFailure(ctx, err)
		}
	}
	return waitResult(current, current.Status != first.Status, start), nil
}

// waiting is whether a is still in a state worth waiting on: pending, for a
// decision. Anything else is reported at once.
func waiting(s approval.Status) bool { return s == approval.Pending }

func waitResult(a approval.Approval, changed bool, start time.Time) approvalWaitResult {
	return approvalWaitResult{Approval: cerbapi.ViewOfApproval(a), Changed: changed,
		Waited: time.Since(start).Round(time.Second).String(), NextStep: waitNextStep(a).Error()}
}

func waitNextStep(a approval.Approval) error {
	switch a.Status {
	case approval.Pending:
		return redact.Guidance("still pending; call cerberus_approval_wait again with id %s to keep waiting, or remind your operator (`%s`)", a.ID, a.ApproveWith())
	case approval.Approved:
		return redact.Guidance("approved: retry the call that asked for it, with the same arguments and approval_id %s. It can be used once", a.ID)
	case approval.Denied:
		return redact.Guidance("denied by your operator: do not retry; ask them what they would like instead")
	case approval.Expired:
		return redact.Guidance("expired unused: retry the call without approval_id to ask again")
	case approval.Consumed:
		return redact.Guidance("already used by a call; a new call needs a new approval")
	case approval.Revoked:
		return redact.Guidance("revoked by your operator before it was used: do not retry; ask them why")
	}
	return fmt.Errorf("approval %s is %s", a.ID, a.Status)
}
