package cerbapi

import (
	"context"
	"fmt"

	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
)

// EnsureFresher is the narrow capability set EnsureFresh composes. Both
// *SocketClient (daemon path) and *ResourceRuntimeService (in-process path)
// satisfy it, so EnsureFresh runs against either without knowing which.
type EnsureFresher interface {
	GetResourceRuntime(ctx context.Context, id string) (*ResourceRuntimeStatus, error)
	DeployResource(ctx context.Context, id string, opts ...MutationOption) (*OpResult, error)
	ApplyResource(ctx context.Context, id string, opts ...MutationOption) (*OpResult, error)
	SyncResource(ctx context.Context, id string, opts ...MutationOption) (*OpResult, error)
}

// EnsureFreshResult records the action EnsureFresh chose and its outcome.
type EnsureFreshResult struct {
	ServiceID string    `json:"service_id"`
	Result    *OpResult `json:"result,omitempty"`
	// Action is the verb EnsureFresh ran: deploy | apply | sync | noop.
	// A noop with Success=false means verification or unsupported advice
	// prevented a lifecycle action; it is not a freshness confirmation.
	Action string `json:"action"`
	// Reason explains why an action or further verification was needed.
	Reason string `json:"reason,omitempty"`
	// Message is the underlying op's message (or error text on failure).
	Message string `json:"message,omitempty"`
	Success bool   `json:"success"`
}

// EnsureFresh reconciles built artifacts using runtime advice. Source edits
// are not inspected: after editing code use force or DeployResource to build.
// opts go to whichever mutation it chooses, so the caller's acknowledgment
// covers exactly that one call; EnsureFresh never acknowledges on its own.
func EnsureFresh(ctx context.Context, f EnsureFresher, id string, force bool, opts ...MutationOption) (*EnsureFreshResult, error) {
	if force {
		op, err := f.DeployResource(ctx, id, opts...)
		return ensureFreshResult(id, "deploy", "forced rebuild", op), err
	}

	st, err := f.GetResourceRuntime(ctx, id)
	if err != nil {
		return nil, err
	}

	switch st.RecommendedAction {
	case "deploy":
		op, err := f.DeployResource(ctx, id, opts...)
		return ensureFreshResult(id, "deploy", st.RecommendedReason, op), err
	case "apply":
		op, err := f.ApplyResource(ctx, id, opts...)
		return ensureFreshResult(id, "apply", st.RecommendedReason, op), err
	case "sync":
		op, err := f.SyncResource(ctx, id, opts...)
		return ensureFreshResult(id, "sync", st.RecommendedReason, op), err
	case "":
		msg := "no built-binary drift detected; source freshness is not checked; use deploy or ensure-fresh --force after source changes"
		return &EnsureFreshResult{ServiceID: id, Action: "noop", Message: msg, Success: true}, nil
	case "inspect":
		return &EnsureFreshResult{ServiceID: id, Action: "noop", Reason: st.RecommendedReason,
			Message: localconn.RecommendedNextStep("inspect", st.RecommendedReason), Success: false}, nil
	default:
		return &EnsureFreshResult{ServiceID: id, Action: "noop", Reason: st.RecommendedReason,
			Message: fmt.Sprintf("unsupported recommended action %q; inspect the resource before changing it", st.RecommendedAction), Success: false}, nil
	}
}

func ensureFreshResult(id, action, reason string, op *OpResult) *EnsureFreshResult {
	r := &EnsureFreshResult{ServiceID: id, Action: action, Reason: reason, Result: op}
	if op != nil {
		r.Success = op.Success
		r.Message = op.Message
		if !op.Success && op.Error != "" {
			r.Message = op.Error
		}
	}
	return r
}
