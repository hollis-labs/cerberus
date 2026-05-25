package cerbapi

import "context"

// EnsureFresher is the narrow capability set EnsureFresh composes. Both
// *SocketClient (daemon path) and *ResourceRuntimeService (in-process path)
// satisfy it, so EnsureFresh runs against either without knowing which.
type EnsureFresher interface {
	GetResourceRuntime(ctx context.Context, id string) (*ResourceRuntimeStatus, error)
	DeployResource(ctx context.Context, id string, opts ...DeployResourceOption) (*OpResult, error)
	ApplyResource(ctx context.Context, id string) (*OpResult, error)
	SyncResource(ctx context.Context, id string) (*OpResult, error)
}

// EnsureFreshResult records the action EnsureFresh chose and its outcome.
type EnsureFreshResult struct {
	ServiceID string `json:"service_id"`
	// Action is the verb EnsureFresh ran: deploy | apply | sync | noop.
	Action string `json:"action"`
	// Reason is the staleness reason that drove the choice (empty for noop).
	Reason string `json:"reason,omitempty"`
	// Message is the underlying op's message (or error text on failure).
	Message string `json:"message,omitempty"`
	Success bool   `json:"success"`
}

// EnsureFresh idempotently makes a resource match its current source by
// executing the action Cerberus already recommends from runtime status —
// deploy (rebuild + sync + activate), apply, or sync — or nothing when the
// resource is already current. It exists to remove the
// deploy-vs-apply-vs-reload choice, which is the most common cause of a
// running service silently lagging its source: callers just ask for "fresh".
//
// force makes it always deploy. Use force for resources whose mode has no
// staleness detection yet (mode: dev_session) when you know the source
// changed — without it, EnsureFresh cannot tell a dev_session is stale and
// will no-op.
func EnsureFresh(ctx context.Context, f EnsureFresher, id string, force bool, opts ...DeployResourceOption) (*EnsureFreshResult, error) {
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
		op, err := f.ApplyResource(ctx, id)
		return ensureFreshResult(id, "apply", st.RecommendedReason, op), err
	case "sync":
		op, err := f.SyncResource(ctx, id)
		return ensureFreshResult(id, "sync", st.RecommendedReason, op), err
	default:
		// No recommended action: either already current (os_service +
		// run_from: artifact) or a mode with no staleness detection yet
		// (dev_session). Either way there is nothing safe to do without an
		// explicit --force.
		msg := "already current"
		if st.Mode == "dev_session" {
			msg = "no staleness detection for dev_session; rerun with --force to rebuild"
		}
		return &EnsureFreshResult{ServiceID: id, Action: "noop", Message: msg, Success: true}, nil
	}
}

func ensureFreshResult(id, action, reason string, op *OpResult) *EnsureFreshResult {
	r := &EnsureFreshResult{ServiceID: id, Action: action, Reason: reason}
	if op != nil {
		r.Success = op.Success
		r.Message = op.Message
		if !op.Success && op.Error != "" {
			r.Message = op.Error
		}
	}
	return r
}
