package cerbapi

import (
	"context"
	"errors"

	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/pipeline"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/scheduling"
)

// ScheduledExecutor adapts scheduled targets to the existing shared runtime.
// It is not installed in a daemon or exposed on any caller surface by this task.
type ScheduledExecutor struct{ Runtime *ResourceRuntimeService }

type scheduledDeliveryKey struct{}

func (e ScheduledExecutor) ExecuteDelivery(ctx context.Context, target scheduling.Target, admit scheduling.Admission, delivery *scheduling.Delivery) error {
	if target.Kind != scheduling.PipelineRun || delivery == nil {
		return redact.Guidance("per-fire environment and capture are unavailable for this target; nothing was sent")
	}
	return e.Execute(context.WithValue(ctx, scheduledDeliveryKey{}, delivery), target, admit)
}

type scheduledAdmissionKey struct{}
type scheduledGateKey struct{}
type scheduledGate struct {
	spec    auditSpec
	call    *auditCall
	recheck func() error
}
type scheduledAdmission struct {
	target scheduling.Target
	admit  scheduling.Admission
}

func scheduledTargetChanged(spec auditSpec) error {
	return externalConnectorError(ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}, ExternalConnectorPlanStale, redact.Guidance("scheduled target definition changed before effect admission; nothing was sent"))
}

func withScheduledGate(ctx context.Context, spec auditSpec, call *auditCall, recheck func() error) context.Context {
	return context.WithValue(ctx, scheduledGateKey{}, scheduledGate{spec: spec, call: call, recheck: recheck})
}

func admitLockedScheduledEffect(ctx context.Context) error {
	gate, ok := ctx.Value(scheduledGateKey{}).(scheduledGate)
	if !ok {
		if CallerSurfaceFrom(ctx) == SurfaceScheduler {
			return redact.Guidance("scheduled runtime gate is missing; nothing was sent")
		}
		return nil
	}
	return admitScheduledEffect(ctx, gate.spec, gate.call)
}

func (e ScheduledExecutor) Execute(ctx context.Context, target scheduling.Target, admit scheduling.Admission) error {
	if e.Runtime == nil || admit == nil {
		return redact.Guidance("scheduled runtime admission is unavailable; nothing was sent")
	}
	// Override every inherited caller claim. Scheduler automation is gated as
	// acting for an agent; it never receives the supervisor's exemption.
	ctx = BeginRequest(ctx, SurfaceScheduler)
	if dispatch, ok := scheduling.DispatchFrom(ctx); ok {
		p, _ := PrincipalFrom(ctx)
		p.Session, p.OnBehalfOf = dispatch.FireID, dispatch.JobKey
		ctx = WithPrincipal(ctx, p)
	}
	ctx = context.WithValue(ctx, scheduledAdmissionKey{}, scheduledAdmission{target: target, admit: admit})
	// This acknowledgment lasts for this call only. Admission still needs a
	// fresh exact-fire permit and the runtime's policy/approval gates.
	opts := []MutationOption{WithAcknowledged(true)}
	switch target.Kind {
	case scheduling.ResourceStart:
		out, err := e.Runtime.ApplyResource(ctx, target.ID, opts...)
		return resultError(err, out == nil || !out.Success)
	case scheduling.ResourceDeploy:
		out, err := e.Runtime.DeployResource(ctx, target.ID, opts...)
		return resultError(err, out == nil || !out.Success)
	case scheduling.PipelineRun:
		out, err := e.Runtime.RunPipeline(ctx, target.ID, opts...)
		return scheduledPipelineResultError(out, err)
	default:
		return redact.Guidance("unsupported scheduled target; nothing was sent")
	}
}

func scheduledPipelineResultError(out *PipelineRunResult, runErr error) error {
	if runErr != nil || out == nil || !out.Success {
		return resultError(runErr, true)
	}
	execution, decodeErr := out.Execution()
	if decodeErr != nil {
		return decodeErr
	}
	if execution.OutcomeUnknown {
		return redact.Guidance("pipeline output process lifetime is uncertain")
	}
	if execution.Status != domain.StateHealthy {
		return &ExternalConnectorError{Code: ExternalConnectorOperationFailed, Err: scheduling.ErrExecutionFailed}
	}
	return nil
}

// admitScheduledEffect runs after the ordinary runtime gate, on its checked
// snapshot and before any mutation. Unmarked calls keep their existing gates;
// a scheduler-marked call missing the trusted admission callback fails closed.
func admitScheduledEffect(ctx context.Context, spec auditSpec, call *auditCall) error {
	a, marked := ctx.Value(scheduledAdmissionKey{}).(scheduledAdmission)
	if !marked && CallerSurfaceFrom(ctx) != SurfaceScheduler {
		return nil
	}
	args := ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}
	refuse := func() error {
		return externalConnectorError(args, ExternalConnectorPolicyDenied, redact.Guidance("scheduled effect has no matching trusted admission; nothing was sent"))
	}
	p, ok := PrincipalFrom(ctx)
	if !marked || a.admit == nil || CallerSurfaceFrom(ctx) != SurfaceScheduler || !ok || p.Kind != PrincipalAutomation || p.ActingFor != PrincipalAgent || spec.automation || spec.plan == nil || spec.planOnly || spec.dryRun || spec.breakGlass != nil || spec.confirmedPlanHash != "" {
		return refuse()
	}
	connector, operation := "", ""
	switch a.target.Kind {
	case scheduling.ResourceStart:
		connector, operation = "local", localconn.OpApply
	case scheduling.ResourceDeploy:
		connector, operation = "local", localconn.OpDeploy
	case scheduling.PipelineRun:
		connector, operation = "pipeline", pipeline.OpRun
	}
	if spec.connector != connector || spec.operation != operation || spec.config["id"] != a.target.ID {
		return refuse()
	}
	gate, hasGate := ctx.Value(scheduledGateKey{}).(scheduledGate)
	if hasGate && gate.recheck != nil {
		if staleErr := gate.recheck(); staleErr != nil {
			return staleErr
		}
	}
	pending, err := spec.plan(ctx)
	if err != nil {
		return err
	}
	hash, err := pending.Hash()
	if err != nil {
		return err
	}
	call.intent.PlanHash = hash
	err = a.admit(ctx, hash, func() error {
		// Authorizers may take time. Brakes and policy are read freshly again
		// immediately before the receipt is marked sent. Scheduler policy is
		// enforced even if the host's ordinary enforcement remains in shadow.
		_, resolved := auditTarget(spec)
		if brakeErr := brakeRefusal(ctx, spec, resolved, false); brakeErr != nil {
			return brakeErr
		}
		if scopeErr := scopeRefusal(ctx, spec); scopeErr != nil {
			return scopeErr
		}
		if hasGate && gate.recheck != nil {
			if staleErr := gate.recheck(); staleErr != nil {
				return staleErr
			}
		}
		freshPlan, planErr := spec.plan(ctx)
		if planErr != nil {
			return planErr
		}
		freshHash, hashErr := freshPlan.Hash()
		if hashErr != nil {
			return hashErr
		}
		if freshHash != hash {
			return externalConnectorError(args, ExternalConnectorPlanStale, redact.Guidance("scheduled target plan changed during authorization; nothing was sent"))
		}
		return enforceDecision(ctx, call, spec, policyRequest(ctx, spec, resolved))
	})
	if err != nil {
		var coded *ExternalConnectorError
		if errors.As(err, &coded) {
			return err
		}
		return externalConnectorError(args, ExternalConnectorPolicyDenied, err)
	}
	return nil
}
