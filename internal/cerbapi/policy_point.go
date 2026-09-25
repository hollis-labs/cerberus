package cerbapi

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The process's policy decision point. The daemon and the in-process CLI
// install the applied snapshot at start (app.LoadPolicy); until then, and
// in a test that installs none, it is the built-in baseline.
var policyPoint atomic.Pointer[pdpHolder]

type pdpHolder struct{ pdp policy.PDP }

// SetPolicyDecisionPoint installs the decision point every gated operation
// is authorized against.
func SetPolicyDecisionPoint(pdp policy.PDP) {
	if pdp == nil {
		pdp = policy.BaselineOnly(policy.SnapshotBaseline)
	}
	policyPoint.Store(&pdpHolder{pdp: pdp})
}

// PolicyDecisionPoint is the installed decision point.
func PolicyDecisionPoint() policy.PDP {
	if h := policyPoint.Load(); h != nil {
		return h.pdp
	}
	return policy.BaselineOnly(policy.SnapshotBaseline)
}

// policyRequest is the operation as the decision point sees it.
func policyRequest(ctx context.Context, spec auditSpec, t target.Target) policy.Request {
	r := policy.Request{Connector: spec.connector, Operation: spec.operation, Target: t}
	if spec.known {
		r.Effect = spec.op.Effect
		r.EffectUndeclared = spec.op.EffectUndeclared
	}
	// A dry run counts as the plan step only where there is a preview.
	r.DryRun = spec.dryRun && spec.op.Preview != contract.PreviewNone
	if p, ok := PrincipalFrom(ctx); ok {
		r.Principal = policy.Principal{Kind: string(p.Kind), Client: p.Client, Session: p.Session}
	}
	return r
}

// shadowDecision authorizes the operation and returns the result for its
// record, and the posture it was evaluated under. In P2 nothing reads it to
// decide: it is recorded, never enforced.
func shadowDecision(ctx context.Context, spec auditSpec, t target.Target) (*audit.PolicyDecision, string) {
	res := PolicyDecisionPoint().Authorize(policyRequest(ctx, spec, t))
	out := &audit.PolicyDecision{Decision: string(res.Decision), WouldBlock: res.WouldBlock, Snapshot: res.Snapshot, Shadow: true,
		MatchedRules: make([]audit.MatchedRule, 0, len(res.Matched))}
	for _, m := range res.Matched {
		out.MatchedRules = append(out.MatchedRules, audit.MatchedRule{Rule: m.Rule, Decision: string(m.Decision), Reason: m.Reason})
	}
	posture := res.Posture
	if posture == "" {
		posture = policy.PostureSecure
	}
	return out, posture
}

// PolicySnapshotChanged is the outcome code of a policy load that found the
// applied snapshot no longer matching the hash `cerberus policy apply`
// recorded. The decision point fell back to the baseline.
const PolicySnapshotChanged = "policy_snapshot_changed"

// RecordPolicyLoad writes a policy load's records when it found a mismatch:
// an intent and an outcome coded policy_snapshot_changed, naming both hashes.
// It is Cerberus acting on its own, so it is never refused; a write failure
// is only logged by the sink's caller.
func RecordPolicyLoad(sink audit.Sink, status policy.LoadStatus) error {
	if sink == nil || !status.Mismatch() {
		return nil
	}
	principal := audit.Principal{Kind: audit.PrincipalAutomation, Surface: string(SurfaceUnknown), Via: "policy"}
	targetRec := audit.Target{Kind: "policy.snapshot", Fields: map[string]string{"recorded": status.Recorded, "found": status.Found}}
	id := audit.NewID()
	rec := audit.Record{Kind: audit.KindIntent, OperationID: id, Principal: principal, Connector: "policy", Operation: "load",
		Effect: string(contract.EffectAdmin), Target: targetRec, Reason: status.Problem, Posture: audit.PostureSecure}
	if _, err := sink.Write(rec); err != nil {
		return err
	}
	rec.Kind, rec.ID = audit.KindOutcome, ""
	rec.Decision, rec.OutcomeCode = audit.DecisionAllowed, PolicySnapshotChanged
	_, err := sink.Write(rec)
	return err
}

// ApplyPolicy writes file as the applied snapshot, recorded as an admin
// event before it changes anything: an unwritable log applies nothing. It
// is in-process only — policy never changes through the socket, the web
// console or MCP (Decision 10) — and the caller has shown the flips and
// taken the operator's typed confirmation on a terminal.
func ApplyPolicy(ctx context.Context, sink audit.Sink, store policy.Store, file policy.File, flips int) (_ string, retErr error) {
	if surface := CallerSurfaceFrom(ctx); surface != SurfaceInProcess {
		return "", externalConnectorError(ExternalConnectorOperationArgs{Connector: "policy", Operation: "apply"}, ExternalConnectorUnsupported,
			redact.Guidance("policy is changed only by `cerberus policy apply` in your terminal, never from the %s surface", surface))
	}
	data, err := policy.Encode(file)
	if err != nil {
		return "", err
	}
	hash := policy.Hash(data)
	op := contract.Operation{Name: "apply", Effect: contract.EffectAdmin, Target: contract.TargetDescriptor{Kind: "policy.snapshot", From: []string{"hash"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSWrites}.Finalize()
	call, err := beginGated(ctx, sink, slog.Default(), auditSpec{connector: "policy", operation: "apply", op: op, known: true, acknowledged: true,
		config: map[string]any{"hash": hash, "flips": flips}})
	if err != nil {
		return "", err
	}
	defer func() { call.finish(retErr) }()
	written, err := store.Apply(file)
	if err != nil {
		return "", err
	}
	return written, nil
}
