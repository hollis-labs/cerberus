package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/presence"
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
}

var brakesPoint atomic.Pointer[Brakes]

// SetBrakes installs the brakes the gate reads; nil removes them.
func SetBrakes(b *Brakes) { brakesPoint.Store(b) }

// ProcessBrakes is the installed brakes, or nil.
func ProcessBrakes() *Brakes { return brakesPoint.Load() }

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
	recorded, recordedProblems := brake.Recorded(b.AuditDir)
	problems = append(problems, recordedProblems...)
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
func brakeRefusal(ctx context.Context, spec auditSpec, resolved target.Target, dryRun bool) error {
	b := ProcessBrakes()
	preview := spec.pluginPreview()
	if b == nil || unbraked[spec.connector] || (dryRun && !preview) || spec.planOnly {
		return nil
	}
	effect := spec.op.Effect
	if !spec.known {
		effect = contract.EffectExec
	}
	if preview {
		effect = contract.EffectReadSensitive
	}
	if refusal := suspensionRefusal(ctx, spec, effect, dryRun && !preview); refusal != nil {
		return refusal
	}
	blocked, lockdown, freeze := b.Current().Blocks(spec.connector, effect, resolved)
	if !blocked {
		return nil
	}
	args := ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}
	if lockdown != nil {
		return externalConnectorError(args, ExternalConnectorLockdown,
			redact.Guidance("Cerberus is in LOCKDOWN since %s (engaged by %s over %s%s): only plain reads run, so %s %s did not. A person lifts it with `cerberus lockdown --off`",
				lockdown.EngagedAt.Local().Format(time.RFC3339), lockdown.By.Kind, lockdown.By.Via, reasonSuffix(lockdown.Reason), spec.connector, spec.operation))
	}
	return externalConnectorError(args, ExternalConnectorFrozen,
		redact.Guidance("%s is FROZEN by %s since %s (engaged by %s over %s%s): only plain reads run on it, so %s %s did not. A person lifts it with `cerberus freeze --off %s`",
			resolved.ID, freeze.ID, freeze.EngagedAt.Local().Format(time.RFC3339), freeze.By.Kind, freeze.By.Via, reasonSuffix(freeze.Reason), spec.connector, spec.operation, freeze.ID))
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}

// recordBrakes writes brake_changed with the state after a change, and
// notifies.
func recordBrakes(ctx context.Context, sink audit.Sink, operation, note string, st brake.State, t audit.Target) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	rec := audit.Record{Kind: audit.KindBrakeChanged, Principal: principalFor(ctx, auditSpec{}), Connector: "brake", Operation: operation,
		Effect: string(contract.EffectAdmin), Target: t, Note: note, Brakes: data, Posture: audit.PostureSecure}
	if _, err := sink.Write(rec); err != nil {
		return err
	}
	notify("Cerberus: "+note, "")
	return nil
}

// EngageLockdown puts Cerberus in lockdown: engaging is trivially easy, for
// anyone, from anywhere (an agent may stop itself).
func EngageLockdown(ctx context.Context, sink audit.Sink, store brake.Store, reason string) (brake.State, brake.Lockdown, error) {
	st, l, err := store.EngageLockdown(principalFor(ctx, auditSpec{}), reason)
	if err != nil {
		return st, brake.Lockdown{}, err
	}
	if rerr := recordBrakes(ctx, sink, "lockdown", "LOCKDOWN engaged"+reasonSuffix(reason), st, audit.Target{Kind: "brake.lockdown", Fields: map[string]string{"id": l.ID}}); rerr != nil {
		return st, *l, rerr
	}
	return st, *l, nil
}

// EngageFreeze freezes the targets match selects.
func EngageFreeze(ctx context.Context, sink audit.Sink, store brake.Store, match policy.TargetMatch, reason string) (brake.State, brake.Freeze, error) {
	st, f, err := store.EngageFreeze(match, principalFor(ctx, auditSpec{}), reason)
	if err != nil {
		return st, brake.Freeze{}, err
	}
	note := fmt.Sprintf("FREEZE %s engaged on %s%s", f.ID, match.String(), reasonSuffix(reason))
	if rerr := recordBrakes(ctx, sink, "freeze", note, st, audit.Target{Kind: "brake.freeze", Fields: map[string]string{"id": f.ID}}); rerr != nil {
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
	args := ExternalConnectorOperationArgs{Connector: "brake", Operation: operation}
	if err != nil {
		return externalConnectorError(args, ExternalConnectorAuditUnavailable,
			redact.Guidance("the audit log could not be read, so a lift could not be recorded where it counts; nothing was lifted: %v", err))
	}
	return externalConnectorError(args, ExternalConnectorAuditUnavailable,
		redact.Guidance("the audit log's chain does not verify (%d problem(s)), and a lift recorded past the break is not applied; nothing was lifted. Check it with `cerberus audit verify`, then a person runs `cerberus audit reanchor` and lifts again", len(checked.Problems)))
}

// LiftLockdown lifts the lockdown, protected (liftProof).
func LiftLockdown(ctx context.Context, sink audit.Sink, store brake.Store, approvalID string) (brake.State, error) {
	cur := currentBrakes(store)
	if cur.Lockdown == nil {
		return cur, brake.ErrNotEngaged
	}
	if err := liftUntrusted(sink, "lift_lockdown"); err != nil {
		return cur, err
	}
	t := audit.Target{Kind: "brake.lockdown", Fields: map[string]string{"id": cur.Lockdown.ID}}
	proof, err := liftProof(ctx, "lift_lockdown", t, cur.Lockdown.ID, approvalID)
	if err != nil {
		return cur, err
	}
	st, err := store.LiftLockdown(cur.Lockdown.ID, principalFor(ctx, auditSpec{}), proof)
	if err != nil {
		return st, err
	}
	return st, recordBrakes(ctx, sink, "lift_lockdown", "LOCKDOWN lifted ("+proof+")", st, t)
}

// LiftFreeze lifts the freeze with id, protected (liftProof).
func LiftFreeze(ctx context.Context, sink audit.Sink, store brake.Store, id, approvalID string) (brake.State, error) {
	cur := currentBrakes(store)
	if _, ok := cur.Freeze(id); !ok {
		return cur, brake.ErrNotEngaged
	}
	if err := liftUntrusted(sink, "lift_freeze"); err != nil {
		return cur, err
	}
	t := audit.Target{Kind: "brake.freeze", Fields: map[string]string{"id": id}}
	proof, err := liftProof(ctx, "lift_freeze", t, id, approvalID)
	if err != nil {
		return cur, err
	}
	st, err := store.LiftFreeze(id, principalFor(ctx, auditSpec{}), proof)
	if err != nil {
		return st, err
	}
	return st, recordBrakes(ctx, sink, "lift_freeze", "FREEZE "+id+" lifted ("+proof+")", st, t)
}

// currentBrakes is the effective state, through the installed brakes when
// they read this store.
func currentBrakes(store brake.Store) brake.State {
	if b := ProcessBrakes(); b != nil && b.Store.Dir == store.Dir {
		return b.Current()
	}
	st, _ := store.Load()
	return st
}

// liftProof is what authorizes lifting a brake. A person lifts it, never an
// agent or MCP. With a passkey enrolled, it is an out-of-band approval, met
// with the passkey on the console: the first call asks for it and answers
// approval_pending; the call naming it lifts. With no passkey enrolled, a
// person at the CLI lifts it on their terminal (the CLI asks for a typed
// phrase): the floor, and loud.
func liftProof(ctx context.Context, operation string, t audit.Target, id, approvalID string) (string, error) {
	args := ExternalConnectorOperationArgs{Connector: "brake", Operation: operation}
	p := principalFor(ctx, auditSpec{})
	if p.Kind != string(PrincipalHuman) || strings.HasPrefix(p.Via, "mcp") {
		return "", externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.Guidance("a brake is lifted by a person, and this caller is %s over %s; engaging is open to anyone, lifting is not", p.Kind, p.Via))
	}
	// The terminal floor is for an operator who never enrolled a key: the
	// registry opened, matches what the audit log recorded, and holds none.
	// A registry that cannot be read, or changed outside enrollment (a
	// deleted file included), refuses the lift; it never falls to the floor.
	pres := ProcessPresence()
	if pres == nil {
		return "", externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.Guidance("passkey store unreadable: this daemon has no passkey service, so it cannot tell whether a key is enrolled, and a brake is not lifted on a guess; check the daemon log for daemon.approvals.open_failed, repair ~/.cerberus/approvals, restart the daemon, then lift again"))
	}
	st := pres.Status()
	switch st.State {
	case presence.StateNotSetUp:
		if p.Via != ViaCLI {
			return "", externalConnectorError(args, ExternalConnectorApprovalRequired,
				redact.Guidance("with no passkey enrolled, a brake is lifted on a terminal: run `cerberus lockdown --off` (or `cerberus freeze --off <id>`); enroll one with `cerberus approvals enroll`"))
		}
		return "tty", nil
	case presence.StateOK:
	case presence.StateCooldown:
		if !st.CooldownUntil.IsZero() {
			return "", externalConnectorError(args, ExternalConnectorApprovalRequired,
				redact.Guidance("the passkey registry changed outside `cerberus approvals enroll`, so a brake is not lifted until the cool-down ends at %s; `cerberus approvals keys` shows the registry", st.CooldownUntil.Local().Format(time.RFC1123)))
		}
		fallthrough
	default:
		return "", externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.Guidance("passkey store unreadable: the passkey registry under ~/.cerberus/approvals cannot be read, so a brake is not lifted on a guess; repair its permissions or contents, then lift again"))
	}
	broker := ProcessBroker()
	if broker == nil {
		return "", externalConnectorError(args, ExternalConnectorApprovalPending,
			redact.Guidance("lifting a brake with a passkey needs the daemon; start it with `cerberus daemon`"))
	}
	if approvalID == "" {
		intent := audit.Record{OperationID: audit.NewID(), Principal: p, Connector: "brake", Operation: operation, Effect: string(contract.EffectAdmin), Target: t, ArgsDigest: id}
		a, err := broker.request(ctx, intent, policy.Result{Matched: []policy.Match{{Rule: "brake.lift", Decision: policy.Approve, Reason: "a brake is lifted with a passkey"}}},
			approval.ChannelOutOfBand, approval.ScopeOnce, 30*time.Minute, planSnapshot{}, nil)
		if err != nil {
			return "", externalConnectorError(args, ExternalConnectorAuditUnavailable, err)
		}
		pending := externalConnectorError(args, ExternalConnectorApprovalPending,
			redact.Guidance("lifting %s is approved with your passkey: approve %s on the console (`%s` opens it), then lift again with --approval %s", id, a.ID, a.ApproveWith(), a.ID))
		var coded *ExternalConnectorError
		if errors.As(pending, &coded) {
			coded.Approval = &ApprovalRef{ID: a.ID, ExpiresAt: a.ExpiresAt, ApproveWith: a.ApproveWith(), Channel: a.Channel}
		}
		return "", pending
	}
	// A passkey is enrolled, so the lift needs a passkey approval: the
	// channel is required here, never read from the store, where a
	// same-uid process could write an approval met on a terminal (H-b).
	a, err := broker.Consume(ctx, approvalID, approval.ConsumeCheck{Connector: "brake", Operation: operation, Principal: p, Target: t, ArgsDigest: id, RequireOutOfBand: true})
	if err != nil {
		return "", consumeRefusal(args, approvalID, a, err)
	}
	return "passkey:" + a.ID, nil
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

// BrakeLiftArgs lifts one, with the passkey approval that authorizes it.
type BrakeLiftArgs struct {
	ApprovalID string `json:"approval_id,omitempty"`
}

// handleBrakes is GET /brakes, POST /brakes/lockdown, /brakes/lockdown/lift,
// /brakes/freeze and /brakes/freeze/{id}/lift.
func (s *SocketServer) handleBrakes(w http.ResponseWriter, r *http.Request) {
	b := ProcessBrakes()
	if b == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "this daemon has no brakes installed")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/brakes"), "/")
	if r.Method == http.MethodGet && rest == "" {
		writeJSON(w, http.StatusOK, BrakesView{State: b.Current(), Problems: b.Problems()})
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var (
		st  brake.State
		err error
	)
	switch {
	case rest == "lockdown" || rest == "freeze":
		var args BrakeEngageArgs
		if err = decodeJSONBody(r, &args); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if rest == "lockdown" {
			st, _, err = EngageLockdown(r.Context(), b.Sink, b.Store, args.Reason)
		} else {
			if args.Match == nil {
				writeJSONError(w, http.StatusBadRequest, "a freeze needs a match")
				return
			}
			st, _, err = EngageFreeze(r.Context(), b.Sink, b.Store, *args.Match, args.Reason)
		}
	case rest == "lockdown/lift" || (strings.HasPrefix(rest, "freeze/") && strings.HasSuffix(rest, "/lift")):
		var args BrakeLiftArgs
		if err = decodeJSONBody(r, &args); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if rest == "lockdown/lift" {
			st, err = LiftLockdown(r.Context(), b.Sink, b.Store, args.ApprovalID)
		} else {
			st, err = LiftFreeze(r.Context(), b.Sink, b.Store, strings.TrimSuffix(strings.TrimPrefix(rest, "freeze/"), "/lift"), args.ApprovalID)
		}
	case strings.HasPrefix(rest, "suspensions/") && strings.HasSuffix(rest, "/reset"):
		var args BrakeResetArgs
		if err = decodeJSONBody(r, &args); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		st, err = ResetSuspension(r.Context(), b.Sink, b.Store, strings.TrimSuffix(strings.TrimPrefix(rest, "suspensions/"), "/reset"), args.Typed)
	default:
		writeJSONError(w, http.StatusNotFound, "expected /brakes, /brakes/lockdown[/lift], /brakes/freeze[/{id}/lift] or /brakes/suspensions/{id}/reset")
		return
	}
	if errors.Is(err, brake.ErrNotEngaged) {
		writeJSONError(w, http.StatusConflict, "that brake is not engaged; `cerberus status` shows what is")
		return
	}
	if err != nil {
		writeServiceError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, BrakesView{State: st})
}

// Brakes is the daemon's brake state.
func (c *SocketClient) Brakes(ctx context.Context) (BrakesView, error) {
	var out BrakesView
	err := c.doJSON(ctx, http.MethodGet, "/brakes", nil, &out)
	return out, err
}

// EngageBrake engages a lockdown, or with a match a freeze.
func (c *SocketClient) EngageBrake(ctx context.Context, args BrakeEngageArgs) (BrakesView, error) {
	path := "/brakes/lockdown"
	if args.Match != nil {
		path = "/brakes/freeze"
	}
	var out BrakesView
	err := c.doJSON(ctx, http.MethodPost, path, args, &out)
	return out, err
}

// LiftBrake lifts the lockdown, or with an id that freeze.
func (c *SocketClient) LiftBrake(ctx context.Context, freezeID string, args BrakeLiftArgs) (BrakesView, error) {
	path := "/brakes/lockdown/lift"
	if freezeID != "" {
		path = "/brakes/freeze/" + url.PathEscape(freezeID) + "/lift"
	}
	var out BrakesView
	err := c.doJSON(ctx, http.MethodPost, path, args, &out)
	return out, err
}

// frozenResource is the freeze covering a local resource, if any.
func frozenResource(res config.ResourceDef) (brake.Freeze, bool) {
	b := ProcessBrakes()
	if b == nil {
		return brake.Freeze{}, false
	}
	labels := res.TargetLabels()
	return b.Current().FreezeCovering("local", target.Resolve("local", "local.resource", res.ID, &labels, false))
}

// pipelineFrozen refuses a pipeline run that would touch a frozen resource:
// the run's definition, as its gate checked it.
func (s *ResourceRuntimeService) pipelineFrozen(snap *pipelineSnapshot) error {
	if snap == nil {
		return nil
	}
	defs := map[string]config.ResourceDef{}
	for _, r := range snap.resources {
		defs[r.ID] = r
	}
	for _, stage := range snap.def.Stages {
		for _, action := range stage.Actions {
			def, ok := defs[action.Resource]
			if !ok {
				continue
			}
			if f, frozen := frozenResource(def); frozen {
				return externalConnectorError(ExternalConnectorOperationArgs{Connector: "pipeline", Operation: "run"}, ExternalConnectorFrozen,
					redact.Guidance("pipeline %q does not run: its %s/%s action touches %s, which is FROZEN by %s%s. A person lifts it with `cerberus freeze --off %s`",
						snap.def.ID, stage.Name, action.Type, def.ID, f.ID, reasonSuffix(f.Reason), f.ID))
			}
		}
	}
	return nil
}
