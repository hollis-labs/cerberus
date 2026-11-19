package gate

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The gate's policy decision point is Options.Policy: the daemon installs
// the applied snapshot at start; until then, and in a test that installs
// none, it is the built-in baseline. The old build kept it in a process
// global (SetPolicyDecisionPoint); here it is a field of the Gate.

// policyRequest is the operation as the decision point sees it.
func policyRequest(ctx context.Context, spec Spec, t target.Target) policy.Request {
	r := policy.Request{Connector: spec.Connector, Operation: spec.Operation, Target: t}
	if spec.Known {
		// A read that touches local files is authorized as a write (H2).
		r.Effect = spec.Op.PolicyEffect()
		r.EffectUndeclared = spec.Op.EffectUndeclared
	}
	// A dry run counts as the plan step only where there is a preview. A
	// plan request is the plan step itself: it runs nothing but the preview.
	r.DryRun = spec.PlanOnly || spec.DryRun && spec.Op.Preview != contract.PreviewNone
	if p, ok := PrincipalFrom(ctx); ok {
		r.Principal = policy.Principal{Kind: string(p.Kind), Client: p.Client, Session: p.Session, ActingFor: string(p.ActingFor)}
		if p.Verified() {
			r.Principal.Subject, r.Principal.Issuer, r.Principal.Verified = p.Subject, p.Issuer, true
		}
	}
	return r
}

// shadowDecision authorizes the operation and returns the result for its
// record, and the posture it was evaluated under. Where enforcement is off
// nothing reads it to decide: it is recorded, never enforced.
func (g *Gate) shadowDecision(ctx context.Context, spec Spec, t target.Target) (*audit.PolicyDecision, string) {
	res := g.authorizeRated(ctx, spec, policyRequest(ctx, spec, t))
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
// is in-process only — policy never changes through the daemon's API, the
// web console or MCP (Decision 10) — and the caller has shown the flips and
// taken the operator's typed confirmation on a terminal.
func (g *Gate) ApplyPolicy(ctx context.Context, store policy.Store, file policy.File, flips int) (_ string, retErr error) {
	if surface := CallerSurfaceFrom(ctx); surface != SurfaceInProcess {
		return "", codedError(opArgs{Connector: "policy", Operation: "apply"}, CodeUnsupported,
			redact.Guidance("policy is changed only by `cerberus policy apply` in your terminal, never from the %s surface", surface))
	}
	data, err := policy.Encode(file)
	if err != nil {
		return "", err
	}
	hash := policy.Hash(data)
	op := contract.Operation{Name: "apply", Effect: contract.EffectAdmin, Target: contract.TargetDescriptor{Kind: "policy.snapshot", From: []string{"hash"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSWrites}.Finalize()
	call, err := g.Begin(ctx, Spec{Connector: "policy", Operation: "apply", Op: op, Known: true, Acknowledged: true,
		Config: map[string]any{"hash": hash, "flips": flips}, Enforcement: file.EnforcementOf().Record(), Snapshot: string(data)})
	if err != nil {
		return "", err
	}
	defer func() { call.Finish(retErr) }()
	written, err := store.Apply(file)
	if err != nil {
		return "", err
	}
	return written, nil
}

// RecordEnforcementChange records, loudly, that what is enforced changed
// (P3-7): an enforcement_changed record with the section before and after,
// and a notification where the gate has a notifier.
func (g *Gate) RecordEnforcementChange(ctx context.Context, before, after policy.Enforcement) error {
	rec := audit.Record{Kind: audit.KindEnforcementChanged, Principal: principalFor(ctx, Spec{}), Connector: "policy", Operation: "enforce",
		Effect: string(contract.EffectAdmin), Note: before.Summary(), Enforcement: after.Record(), Posture: audit.PostureSecure}
	if _, err := g.sink.Write(rec); err != nil {
		return err
	}
	g.notify("Cerberus: enforcement changed", "now "+after.Summary()+" (was "+before.Summary()+")")
	return nil
}

// OwnerPolicyArgs authorize a policy change through the serving Cerberus: the acknowledgment
// (old's typed phrase at the CLI) and, from any caller but the owner on the console, the owner's
// out-of-band approval.
type OwnerPolicyArgs struct {
	Acknowledged bool
	ApprovalID   string
	// What names the change in the refusals ("applying the policy snapshot 0123abcd").
	What string
	// Prepare, when set, writes the working file the snapshot is made from (posture.yaml,
	// enforcement.yaml), once the change is authorized and before it is applied; its restore
	// runs if the apply fails.
	Prepare func() (restore func(), err error)
}

// ApplyOwnerPolicy is ApplyPolicy for the serving Cerberus's policy routes (slice 7d): `cerberus
// policy apply`, `policy enforce`, `posture set` and `posture reset` from the CLI or the console.
// Old applied policy only in-process, after a person typed the confirmation on a terminal
// (Decision 10). No caller is a person at a terminal under decision 0.1, so the authority is the
// owner's, as for secrets set and the brakes: the record is old's (policy apply, effect admin,
// acknowledged, target policy.snapshot, config {hash, flips}, the enforcement and the snapshot),
// written before anything changes; then the owner signed in on the console applies directly, and
// any other caller but an MCP client names the owner's out-of-band approval, bound to the
// snapshot's hash. Where policy also asks to approve the change, that one approval meets it.
func (g *Gate) ApplyOwnerPolicy(ctx context.Context, store policy.Store, file policy.File, flips int, args OwnerPolicyArgs) (_ string, proof string, retErr error) {
	data, err := policy.Encode(file)
	if err != nil {
		return "", "", err
	}
	hash := policy.Hash(data)
	op := contract.Operation{Name: "apply", Effect: contract.EffectAdmin, Target: contract.TargetDescriptor{Kind: "policy.snapshot", From: []string{"hash"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSWrites}.Finalize()
	spec := Spec{Connector: "policy", Operation: "apply", Op: op, Known: true, Acknowledged: args.Acknowledged,
		Config: map[string]any{"hash": hash, "flips": flips}, Enforcement: file.EnforcementOf().Record(), Snapshot: string(data),
		OwnerAuthorized: true}
	if !args.Acknowledged {
		return "", "", codedError(opArgs{Connector: "policy", Operation: "apply"}, CodeAckRequired,
			redact.Guidance("%s changes what policy decides, so it needs an acknowledgment (\"acknowledged\": true: the typed phrase at the CLI); nothing was changed", args.What))
	}
	call, err := g.Begin(ctx, spec)
	if err != nil {
		return "", "", err
	}
	defer func() { call.Finish(retErr) }()
	proof = "console"
	if !ConsolePerson(ctx) || args.ApprovalID != "" || call.PolicyApproves() {
		tgt, _ := AuditTarget(spec)
		if proof, err = call.OwnerApproval(ctx, OwnerApprovalRequest{Connector: "policy", Operation: "apply", Target: tgt,
			ArgsDigest: g.sink.Digest(spec.Config), ApprovalID: args.ApprovalID, What: args.What,
			Rule: "policy.apply", Reason: "policy and the posture are changed by the owner on the console"}); err != nil {
			return "", "", err
		}
	}
	restore := func() {}
	if args.Prepare != nil {
		if restore, err = args.Prepare(); err != nil {
			return "", "", err
		}
	}
	written, err := store.Apply(file)
	if err != nil {
		restore()
		return "", "", err
	}
	return written, proof, nil
}

// AllowsRead reports whether policy lets the caller see one target in a read projection (the
// old build's portfolio allowTarget): where the decision on a read of it is enforced, deny and
// approve both hide it. Nothing is recorded.
func (g *Gate) AllowsRead(ctx context.Context, connector, targetKind, id string, labels *target.ResourceLabels) bool {
	resolved := target.Resolve(connector, targetKind, id, labels, false)
	op := contract.Operation{Name: "read", Effect: contract.EffectRead, Target: contract.TargetDescriptor{Kind: targetKind}}.Finalize()
	req := policyRequest(ctx, Spec{Connector: connector, Operation: "read", Op: op, Known: true, ResolvedTarget: &resolved}, resolved)
	if !g.enforced(req) {
		return true
	}
	d := g.pdp.Authorize(req).Decision
	return d != policy.Deny && d != policy.Approve
}
