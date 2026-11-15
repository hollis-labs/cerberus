package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/budget"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// The answers below are the old build's (internal/mcp on main: budget.go, tools_result.go,
// approvals.go, registry.go), so a client reads them unchanged.

const defaultListLimit = budget.MaxLimit

// budgetedList pages items into the {items,count,total,truncated,hint} envelope.
func budgetedList[T any](tool string, items []T, args map[string]any, hintTemplate string) string {
	if items == nil {
		items = []T{}
	}
	limit := budget.ExtractLimit(args, defaultListLimit)
	_, offset := budget.ExtractPagination(args)
	total := len(items)
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	page := items[offset:end]
	env := budget.Envelope{Items: page, Count: len(page), Total: total, Truncated: len(page) < total}
	if env.Truncated {
		env.Hint = fmt.Sprintf(hintTemplate, total)
		if env.Count > 0 {
			env.Hint += fmt.Sprintf(" Showing %d-%d of %d.", offset+1, end, total)
		}
		env.Hint += " Pass offset/limit (limit max 25) to page through the rest."
		log.Printf("mcp.budget.truncated tool=%s total=%d returned=%d offset=%d limit=%d", tool, total, env.Count, offset, limit)
	}
	data, err := redact.Marshal(env)
	if err != nil {
		return budget.ToolJSON(env)
	}
	return string(data)
}

// indented is a read's answer: the value as indented JSON text.
func indented(v any) (any, error) {
	data, err := redact.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

// lifecycleResult is the part of an OpResult a mutation tool answers with.
type lifecycleResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// toolResult answers a mutation: indented JSON on success, an isError result whose body is the
// result on failure.
func toolResult(r lifecycleResult) (any, error) {
	if !r.Success {
		msg := r.Error
		if msg == "" {
			msg = r.Message
		}
		return nil, toolFailure{message: msg, content: r}
	}
	return indented(r)
}

// toolFailure is an isError result with a JSON body.
type toolFailure struct {
	message string
	content any
	scope   *redact.Scope
}

func (f toolFailure) Error() string { return f.scope.Text(f.message) }

// ToolErrorContent is the isError body.
func (f toolFailure) ToolErrorContent() any {
	data, err := f.scope.Marshal(f.content)
	if err != nil {
		data, _ = json.Marshal(map[string]any{"success": false, "error": f.Error()})
	}
	return json.RawMessage(data)
}

// toolRefusal is the body of a refusal: the OpResult fields a client already reads, the
// refusal's code, for approval_pending the approval to wait for, and the next step.
type toolRefusal struct {
	Success  bool              `json:"success"`
	Error    string            `json:"error"`
	Code     string            `json:"code,omitempty"`
	Approval *gate.ApprovalRef `json:"approval,omitempty"`
	NextStep string            `json:"next_step,omitempty"`
}

// refusalBody is a coded refusal's body.
func refusalBody(r *Refusal, msg string) toolRefusal {
	out := toolRefusal{Success: false, Error: msg, Code: r.Code}
	if r.Approval != nil && r.Approval.ID != "" {
		ref := *r.Approval
		out.Approval = &ref
	}
	if next := nextStep(gate.ErrorCode(r.Code), out.Approval); next != nil {
		out.NextStep = next.Error()
	}
	return out
}

// nextStep is what an agent does after a refusal. Main's text, except that approvals are the
// owner's console session alone (decision 0.1: no passkey, no setup step).
func nextStep(code gate.ErrorCode, ref *gate.ApprovalRef) error {
	switch code {
	case gate.CodeApprovalPending:
		if ref == nil {
			return redact.Guidance("the approval can only be held by the serving Cerberus; ask your operator to start it (`cerberus serve`), then retry this call")
		}
		ask := redact.Guidance("ask your operator to run `%s` in their terminal, where they see the plan and confirm it", ref.ApproveWith)
		if ref.Channel == approval.ChannelOutOfBand {
			ask = redact.Guidance("ask your operator to approve %s on the Cerberus console, signed in, where they see the plan and the arguments of this call", ref.ID)
		}
		return redact.Guidance("%s. You cannot approve it yourself. Then call cerberus_approval_wait with id %s, and once it is approved retry this call with the same arguments and approval_id %s", ask.Error(), ref.ID, ref.ID)
	case gate.CodeApprovalRequired:
		if ref != nil && ref.Channel == approval.ChannelTTYConfirm {
			return redact.Guidance("this needs your operator's confirmation at an interactive terminal, which you cannot give; ask them to run the operation themselves")
		}
		return redact.Guidance("retry this call without approval_id to ask for a new approval")
	case gate.CodeApprovalExpired:
		return redact.Guidance("the approval expired unused; retry this call without approval_id to ask for a new one")
	case gate.CodePlanStale:
		return redact.Guidance("what would run changed since it was approved, so nothing ran; retry without approval_id to see the new plan and ask again")
	case gate.CodePolicyDenied:
		return redact.Guidance("policy denies this call and no approval can change that; do not retry, and tell your operator what you were trying to do")
	case gate.CodeInsufficientScope:
		return redact.Guidance("your token's scopes do not cover this call and you cannot widen them; do not retry, and ask your operator for a token with the scope the error names")
	case gate.CodeSessionSuspended:
		return redact.Guidance("the circuit breaker suspended this session after repeated policy denials; stop, do not retry anything but plain reads, and tell your operator what you were trying to do. Only a person can reset it")
	case gate.CodeLockdown, gate.CodeFrozen:
		return redact.Guidance("an emergency brake is engaged; stop, and tell your operator what you were trying to do. Only a person can lift it")
	default:
		return nil
	}
}

// withRequestScope gives each call its own redaction scope and turns a coded refusal into its
// structured isError body, as main's registry did.
func withRequestScope(h func(context.Context, map[string]any) (any, error)) func(context.Context, map[string]any) (any, error) {
	return func(ctx context.Context, args map[string]any) (any, error) {
		ctx, scope := redact.EnsureScope(ctx)
		result, err := h(ctx, args)
		if err != nil {
			return nil, scopedError(scope, err)
		}
		switch v := result.(type) {
		case nil:
			return nil, nil
		case string:
			return scope.ReplaceValues(v), nil
		default:
			if data, merr := scope.Marshal(v); merr == nil {
				return json.RawMessage(data), nil
			}
			return result, nil
		}
	}
}

func scopedError(scope *redact.Scope, err error) error {
	var f toolFailure
	if errors.As(err, &f) {
		f.scope = scope
		return f
	}
	var r *Refusal
	if errors.As(err, &r) && r.Code != "" {
		return toolFailure{message: r.Error(), content: refusalBody(r, scope.Text(r.Error())), scope: scope}
	}
	return errors.New(scope.ErrorText(err))
}
