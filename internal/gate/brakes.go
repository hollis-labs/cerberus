package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The emergency brake (§12): a lockdown makes Cerberus read-only, a freeze
// makes the targets it matches read-only. They are checked in beginGated
// before policy, in every enforcement mode, shadow included. Changing
// policy, deciding approvals and working the brakes themselves are never
// braked, so the operator can always see, fix and lift.

// Brakes is where the gate reads the brakes from.
type Brakes struct {
	Store    brake.Store
	AuditDir string
	// Sink is where engaging and lifting are recorded.
	Sink audit.Sink

	mu     sync.Mutex
	stamp  string
	state  brake.State
	issues []string
	// recorded is the last brake state read from the audit log, kept for
	// when the log cannot be read at all (H-e).
	recorded brake.State
}

// Current is the effective brake state: the more restrictive of the store
// and the audit log's brake_changed records (brake.Recorded), re-read when
// the store changes.
func (b *Brakes) Current() brake.State {
	stamp := "absent"
	if info, err := os.Stat(filepath.Join(b.Store.Dir, brake.FileName)); err == nil {
		stamp = fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if stamp == b.stamp {
		return b.state
	}
	stored, problems := b.Store.Load()
	recorded, recordedProblems, readErr := brake.RecordedErr(b.AuditDir)
	problems = append(problems, recordedProblems...)
	if readErr != nil {
		// Keep what the log last said, rather than read the brakes from
		// the store alone, where deleting a line would lift one.
		recorded = b.recorded
		problems = append(problems, "the brakes keep the state the audit log last showed")
	} else {
		b.recorded = recorded
	}
	if lacks(stored, recorded) {
		// The log holds a brake the store lost (edited, deleted, or past a
		// break): engaged either way, and written back so a person can
		// lift it, since a lift is checked against the store.
		if restored, err := b.Store.Restore(recorded); err != nil {
			problems = append(problems, "a brake the audit log holds could not be restored to the store: "+err.Error())
		} else {
			stored = restored
			problems = append(problems, "the brake store had lost a brake the audit log holds; it was restored")
			if info, err := os.Stat(filepath.Join(b.Store.Dir, brake.FileName)); err == nil {
				stamp = fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
			}
		}
	}
	b.state, b.issues, b.stamp = brake.Effective(stored, recorded), problems, stamp
	return b.state
}

// lacks reports whether recorded holds a brake the store does not.
func lacks(stored, recorded brake.State) bool {
	if recorded.Lockdown != nil && stored.Lockdown == nil {
		return true
	}
	for _, f := range recorded.Freezes {
		if _, ok := stored.Freeze(f.ID); !ok {
			return true
		}
	}
	for _, x := range recorded.Suspensions {
		_, byID := stored.Suspension(x.ID)
		_, byKey := stored.SuspensionFor(x.Key)
		if !byID && !byKey {
			return true
		}
	}
	return false
}

// Problems are what reading the brakes found wrong.
func (b *Brakes) Problems() []string {
	b.Current()
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.issues...)
}

// unbraked are the connectors the brakes never stop: policy changes,
// approval decisions and the brakes themselves.
var unbraked = map[string]bool{"policy": true, "brake": true, "approvals": true}

// Unbraked reports whether the brakes never stop a connector.
func Unbraked(connector string) bool { return unbraked[connector] }

// brakeRefusal is the brakes' refusal of a call, or nil. A host dry run or a
// plan request runs nothing and passes. A plugin's preview runs the
// plugin's code, with its credentials, so it is braked as the read it is:
// read_sensitive, which a lockdown and a freeze stop and a suspension
// refuses (M8).
func (g *Gate) brakeRefusal(ctx context.Context, spec Spec, resolved target.Target, dryRun bool) error {
	b := g.brakes
	preview := spec.pluginPreview()
	if b == nil || unbraked[spec.Connector] || (dryRun && !preview) || spec.PlanOnly {
		return nil
	}
	effect := spec.Op.Effect
	if !spec.Known {
		effect = contract.EffectExec
	}
	if preview {
		effect = contract.EffectReadSensitive
	}
	if refusal := g.suspensionRefusal(ctx, spec, effect, dryRun && !preview); refusal != nil {
		return refusal
	}
	blocked, lockdown, freeze := b.Current().Blocks(spec.Connector, effect, resolved)
	if !blocked {
		return nil
	}
	args := opArgs{Connector: spec.Connector, Operation: spec.Operation}
	if lockdown != nil {
		return codedError(args, CodeLockdown,
			redact.Guidance("Cerberus is in LOCKDOWN since %s (engaged by %s over %s%s): only plain reads run, so %s %s did not. A person lifts it with `cerberus lockdown --off`",
				lockdown.EngagedAt.Local().Format(time.RFC3339), lockdown.By.Kind, lockdown.By.Via, reasonSuffix(lockdown.Reason), spec.Connector, spec.Operation))
	}
	return codedError(args, CodeFrozen,
		redact.Guidance("%s is FROZEN by %s since %s (engaged by %s over %s%s): only plain reads run on it, so %s %s did not. A person lifts it with `cerberus freeze --off %s`",
			resolved.ID, freeze.ID, freeze.EngagedAt.Local().Format(time.RFC3339), freeze.By.Kind, freeze.By.Via, reasonSuffix(freeze.Reason), spec.Connector, spec.Operation, freeze.ID))
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}

// recordBrakes writes brake_changed with the state after a change, and
// notifies.
func (g *Gate) recordBrakes(ctx context.Context, sink audit.Sink, operation, note string, st brake.State, t audit.Target) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	rec := audit.Record{Kind: audit.KindBrakeChanged, Principal: principalFor(ctx, Spec{}), Connector: "brake", Operation: operation,
		Effect: string(contract.EffectAdmin), Target: t, Note: note, Brakes: data, Posture: audit.PostureSecure}
	if _, err := sink.Write(rec); err != nil {
		return err
	}
	g.notify("Cerberus: "+note, "")
	return nil
}

// errNoBrakes is a brake call on a gate built without brakes.
var errNoBrakes = redact.Guidance("this daemon has no brakes installed")

// brakeStore is the gate's brake store and the sink brake changes are
// recorded in: the brakes' own sink, else the gate's.
func (g *Gate) brakeStore() (audit.Sink, brake.Store, error) {
	if g.brakes == nil {
		return nil, brake.Store{}, errNoBrakes
	}
	sink := g.brakes.Sink
	if sink == nil {
		sink = g.sink
	}
	return sink, g.brakes.Store, nil
}

// EngageLockdown puts Cerberus in lockdown: engaging is trivially easy, for
// anyone, from anywhere (an agent may stop itself).
func (g *Gate) EngageLockdown(ctx context.Context, reason string) (brake.State, brake.Lockdown, error) {
	sink, store, err := g.brakeStore()
	if err != nil {
		return brake.State{}, brake.Lockdown{}, err
	}
	st, l, err := store.EngageLockdown(principalFor(ctx, Spec{}), reason)
	if err != nil {
		return st, brake.Lockdown{}, err
	}
	if rerr := g.recordBrakes(ctx, sink, "lockdown", "LOCKDOWN engaged"+reasonSuffix(reason), st, audit.Target{Kind: "brake.lockdown", Fields: map[string]string{"id": l.ID}}); rerr != nil {
		return st, *l, rerr
	}
	return st, *l, nil
}

// EngageFreeze freezes the targets match selects.
func (g *Gate) EngageFreeze(ctx context.Context, match policy.TargetMatch, reason string) (brake.State, brake.Freeze, error) {
	sink, store, err := g.brakeStore()
	if err != nil {
		return brake.State{}, brake.Freeze{}, err
	}
	st, f, err := store.EngageFreeze(match, principalFor(ctx, Spec{}), reason)
	if err != nil {
		return st, brake.Freeze{}, err
	}
	note := fmt.Sprintf("FREEZE %s engaged on %s%s", f.ID, match.String(), reasonSuffix(reason))
	if rerr := g.recordBrakes(ctx, sink, "freeze", note, st, audit.Target{Kind: "brake.freeze", Fields: map[string]string{"id": f.ID}}); rerr != nil {
		return st, *f, rerr
	}
	return st, *f, nil
}

// liftUntrusted refuses a lift while the audit log's chain does not vouch
// for its tail: the lift's brake_changed record would be written past a
// break, where a lift is not applied (brake.Recorded), so the brake would
// come straight back. A person reanchors the chain first.
func liftUntrusted(sink audit.Sink, operation string) error {
	d, ok := sink.(interface{ Dir() string })
	if !ok {
		return nil
	}
	checked, err := audit.Check(d.Dir())
	if err == nil && checked.TailTrusted() {
		return nil
	}
	args := opArgs{Connector: "brake", Operation: operation}
	if err != nil {
		return codedError(args, CodeAuditUnavailable,
			redact.Guidance("the audit log could not be read, so a lift could not be recorded where it counts; nothing was lifted: %v", err))
	}
	return codedError(args, CodeAuditUnavailable,
		redact.Guidance("the audit log's chain does not verify (%d problem(s)), and a lift recorded past the break is not applied; nothing was lifted. Check it with `cerberus audit verify`, then a person runs `cerberus audit reanchor` and lifts again", len(checked.Problems)))
}

// LiftLockdown lifts the lockdown, protected (liftProof).
func (g *Gate) LiftLockdown(ctx context.Context, approvalID string) (brake.State, error) {
	sink, store, err := g.brakeStore()
	if err != nil {
		return brake.State{}, err
	}
	cur := g.brakes.Current()
	if cur.Lockdown == nil {
		return cur, brake.ErrNotEngaged
	}
	if err = liftUntrusted(sink, "lift_lockdown"); err != nil {
		return cur, err
	}
	t := audit.Target{Kind: "brake.lockdown", Fields: map[string]string{"id": cur.Lockdown.ID}}
	proof, err := g.liftProof(ctx, "lift_lockdown", t, cur.Lockdown.ID, approvalID)
	if err != nil {
		return cur, err
	}
	st, err := store.LiftLockdown(cur.Lockdown.ID, principalFor(ctx, Spec{}), proof)
	if err != nil {
		return st, err
	}
	return st, g.recordBrakes(ctx, sink, "lift_lockdown", "LOCKDOWN lifted ("+proof+")", st, t)
}

// LiftFreeze lifts the freeze with id, protected (liftProof).
func (g *Gate) LiftFreeze(ctx context.Context, id, approvalID string) (brake.State, error) {
	sink, store, err := g.brakeStore()
	if err != nil {
		return brake.State{}, err
	}
	cur := g.brakes.Current()
	if _, ok := cur.Freeze(id); !ok {
		return cur, brake.ErrNotEngaged
	}
	if err = liftUntrusted(sink, "lift_freeze"); err != nil {
		return cur, err
	}
	t := audit.Target{Kind: "brake.freeze", Fields: map[string]string{"id": id}}
	proof, err := g.liftProof(ctx, "lift_freeze", t, id, approvalID)
	if err != nil {
		return cur, err
	}
	st, err := store.LiftFreeze(id, principalFor(ctx, Spec{}), proof)
	if err != nil {
		return st, err
	}
	return st, g.recordBrakes(ctx, sink, "lift_freeze", "FREEZE "+id+" lifted ("+proof+")", st, t)
}

// liftProof is what authorizes lifting a brake. A person lifts it, never an
// agent or MCP, and only with an out-of-band approval decided by the owner
// signed in to the console (OwnerSessionVerifier): the first call asks for
// it and answers approval_pending; the call naming it lifts.
//
// The old build met the approval with a passkey, and lifted on a person's
// terminal alone (proof "tty") when no passkey was enrolled. With passkeys
// gone (decision 0.1) the owner's console session is always the proof, so
// there is no terminal floor: every lift needs the console approval.
func (g *Gate) liftProof(ctx context.Context, operation string, t audit.Target, id, approvalID string) (string, error) {
	return g.ownerApproval(ctx, opArgs{Connector: "brake", Operation: operation}, t, id, approvalID, "lifting "+id)
}

// errOwnerPaths names both ways the owner recovers from a brake (the lead's ruling (a)+(b) on
// decision 0.1): on the console, or from the CLI naming an approval the owner decided there.
const errOwnerPaths = "the owner does it on the console, signed in; or run it from the CLI with --approval <id> once the owner has approved that request on the console"

// ownerApproval is what lifting a brake or resetting a suspension needs: an out-of-band
// approval the owner decided on the console (sealed with the owner session). Any caller but an
// MCP client may ask for one, a person on the console or an operator's key at the CLI, and
// the retry naming it goes ahead once the owner has approved; a person still decides, so the
// decision 0.1 boundary holds. An MCP client is never let through.
func (g *Gate) ownerApproval(ctx context.Context, args opArgs, t audit.Target, id, approvalID, what string) (string, error) {
	p := principalFor(ctx, Spec{})
	if strings.HasPrefix(p.Via, "mcp") {
		return "", codedError(args, CodeApprovalRequired,
			redact.Guidance("%s is not done by an MCP client (this caller is %s over %s): %s", what, p.Kind, p.Via, errOwnerPaths))
	}
	broker := g.broker
	if broker == nil {
		return "", codedError(args, CodeApprovalPending,
			redact.Guidance("%s needs the owner's approval on the console, and this Cerberus holds no approvals (observe mode): %s", what, errOwnerPaths))
	}
	if approvalID == "" {
		intent := audit.Record{OperationID: audit.NewID(), Principal: p, Connector: args.Connector, Operation: args.Operation, Effect: string(contract.EffectAdmin), Target: t, ArgsDigest: id}
		a, err := broker.request(ctx, intent, policy.Result{Matched: []policy.Match{{Rule: "brake." + args.Operation, Decision: policy.Approve, Reason: "a brake is lifted, and a suspension reset, by the owner on the console"}}},
			approval.ChannelOutOfBand, approval.ScopeOnce, 30*time.Minute, PlanSnapshot{}, nil)
		if err != nil {
			return "", codedError(args, CodeAuditUnavailable, err)
		}
		pending := codedError(args, CodeApprovalPending,
			redact.Guidance("%s is approved by the owner on the console: approve %s there, signed in, then run it again with --approval %s", what, a.ID, a.ID))
		var coded *Error
		if errors.As(pending, &coded) {
			coded.Approval = approvalReference(a)
		}
		return "", pending
	}
	// The channel is required here, never read from the store, where a
	// same-uid process could write an approval met on a terminal (H-b); the
	// out-of-band decision's owner-session seal is verified on consume.
	a, err := broker.Consume(ctx, approvalID, approval.ConsumeCheck{Connector: args.Connector, Operation: args.Operation, Principal: p, Target: t, ArgsDigest: id, RequireOutOfBand: true})
	if err != nil {
		return "", consumeRefusal(args, approvalID, a, err)
	}
	return "approval:" + a.ID, nil
}

// BrakesView is the brakes as a surface shows them.
type BrakesView struct {
	State    brake.State `json:"state"`
	Problems []string    `json:"problems,omitempty"`
}

// BrakeEngageArgs engages a lockdown, or with Match a freeze.
type BrakeEngageArgs struct {
	Reason string              `json:"reason,omitempty"`
	Match  *policy.TargetMatch `json:"match,omitempty"`
}

// BrakeLiftArgs lifts one, with the console approval that authorizes it.
type BrakeLiftArgs struct {
	ApprovalID string `json:"approval_id,omitempty"`
}

// FrozenResource is the freeze covering a local resource, if any: what a
// pipeline lane refuses a run that would touch.
func (g *Gate) FrozenResource(res config.ResourceDef) (brake.Freeze, bool) {
	b := g.brakes
	if b == nil {
		return brake.Freeze{}, false
	}
	labels := res.TargetLabels()
	return b.Current().FreezeCovering("local", target.Resolve("local", "local.resource", res.ID, &labels, false))
}
