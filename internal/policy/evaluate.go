package policy

import (
	"fmt"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"

	"github.com/hollis-labs/cerberus/internal/target"
)

// Baseline is the built-in decision per effect class and principal kind
// (section 4, tightened by Decision 14): humans approve every change, agents
// approve everything but a plain read, and admin is the operator's alone.
// Automation takes the agent column (D2): no gated path runs as automation
// today, and one that does is read strictly.
//
// For a human, "approve" is met in P3 by a TTY confirmation that shows the
// plan (Decision 3), which is what --ack stands for today.
var Baseline = map[contract.Effect]map[string]Decision{
	contract.EffectRead:          {"human": Allow, "agent": Allow},
	contract.EffectReadSensitive: {"human": Allow, "agent": Approve},
	contract.EffectWrite:         {"human": Approve, "agent": Approve},
	contract.EffectLifecycle:     {"human": Approve, "agent": Approve},
	contract.EffectDestructive:   {"human": Approve, "agent": Approve},
	contract.EffectExec:          {"human": Approve, "agent": Approve},
	contract.EffectAdmin:         {"human": Approve, "agent": Deny},
}

// changes are the effects Decision 18 keys the admin defaults on.
var changes = []contract.Effect{contract.EffectWrite, contract.EffectLifecycle, contract.EffectDestructive, contract.EffectExec}

// Evaluator is the built-in PDP: the baseline, the target defaults, and an
// operator's policy file.
type Evaluator struct {
	file     File
	snapshot string
}

// NewEvaluator evaluates against file, whose snapshot identity (its hash,
// "baseline" or "mismatch") every result names.
func NewEvaluator(file File, snapshot string) *Evaluator {
	return &Evaluator{file: file, snapshot: snapshot}
}

// BaselineOnly is the evaluator with no operator policy: what applies before
// a snapshot is applied, and after one fails its hash check (D6).
func BaselineOnly(snapshot string) *Evaluator {
	return NewEvaluator(File{Version: FileVersion}, snapshot)
}

var _ PDP = (*Evaluator)(nil)

// File is the policy the evaluator decides with.
func (e *Evaluator) File() File { return e.file }

// GlobalPosture implements PDP.
func (e *Evaluator) GlobalPosture() string { return e.file.GlobalPosture() }

// Authorize evaluates req against every layer and combines the matches.
//
// Under the permissive posture (PostureFor) the built-in strictness steps
// aside: the baseline allows every effect for every principal, and the
// target defaults for prod, unknown, shared and unlabeled-admin targets and
// for ad-hoc targets add nothing. Two things do not relax. A target labeled
// as administered by its owner (admin: owner) is still not ours to change,
// because that is a fact the operator declared about someone else's system,
// not a default. And every operator rule — a baseline cell the file
// replaced, provider, target and principal rules — applies as written.
func (e *Evaluator) Authorize(req Request) Result {
	kind := principalColumn(req.Principal.Kind)
	posture, postureRules := e.file.PostureFor(req)
	permissive := posture == PosturePermissive
	var matches []Match
	add := func(rule string, d Decision, reason string, approval *Approval) {
		matches = append(matches, Match{Rule: rule, Decision: d, Reason: reason, Approval: approval})
	}

	// A plugin operation with no declared effect is exec under secure and
	// write under permissive; the plugin's contract is untouched.
	if req.EffectUndeclared && permissive {
		req.Effect = contract.EffectWrite
	}

	// 1. Baseline, by effect and principal kind.
	effect := effectOf(req)
	switch {
	case effect == "":
		add("baseline.unknown-effect", Deny, "the operation declares no effect class, so nothing allows it", nil)
	case localDevLifecycle(req, effect, kind):
		// Decision 11: day-to-day local work does not prompt a human.
		add("builtin.local-dev-lifecycle", Allow, "a human's lifecycle operation on a local dev resource", nil)
	case permissive && !e.baselineOverridden(effect, kind):
		add(postureRuleName(postureRules), Allow, "the permissive posture: the baseline allows every effect", nil)
	default:
		d := e.baselineCell(effect, kind)
		add(fmt.Sprintf("baseline.%s.%s", effect, kind), d, baselineReason(effect, kind, d), nil)
	}

	// 2. Target defaults (Decisions 17 and 18). Unknown reads as strictly
	// as the labels allow: an unknown admin as owner, an unknown env as
	// prod.
	t := req.Target
	admin := t.AdminFor
	if admin == "" {
		admin = target.AdminUnknown
	}
	if effect != "" && isChange(effect) && (!permissive || admin == target.AdminOwner) {
		switch admin {
		case target.AdminOwner, target.AdminUnknown:
			reason := "the owning team administers this; it is not ours to change on our own say-so"
			if admin == target.AdminUnknown {
				reason = "nobody declared who administers this target, so it is read as the owner's (label it with admin:)"
			}
			add("builtin.admin-"+admin, Deny, reason, nil)
		case target.AdminShared:
			if effect == contract.EffectDestructive || effect == contract.EffectExec {
				add("builtin.admin-shared", Approve, "a shared target: destructive and exec operations need approval", nil)
			}
		}
	}
	if env := t.Env; !permissive && (env == target.EnvProd || env == target.EnvUnknown || env == "") {
		label := "builtin.env-" + string(target.EnvProd)
		reason := "a production target"
		if env != target.EnvProd {
			label = "builtin.env-unknown"
			reason = "no env declared, so it is read as production (label it with env:)"
		}
		switch effect {
		case contract.EffectWrite, contract.EffectLifecycle:
			add(label, Approve, reason, nil)
		case contract.EffectDestructive:
			add(label, Approve, reason+": destructive operations need out-of-band approval", &Approval{Channel: "out_of_band"})
		case contract.EffectRead, contract.EffectReadSensitive, contract.EffectExec, contract.EffectAdmin:
			// Reads are not prod-gated; exec and admin already need approval
			// everywhere.
		}
	}
	if t.Adhoc && !permissive && !e.granted(req.Principal, "adhoc_targets") {
		add("builtin.adhoc", Deny, "the target is named by connection settings, not a registered resource, and this caller lacks the adhoc_targets grant", nil)
	}

	// 3. Provider profiles.
	if p, ok := e.file.Providers[req.Connector]; ok {
		for i, r := range p.Rules {
			if r.matches(req) {
				add(ruleID(r, fmt.Sprintf("providers.%s.rules[%d]", req.Connector, i)), r.Decision, r.Reason, r.Approval)
			}
		}
	}
	// 4. Target rules.
	for i, b := range e.file.Targets {
		if !b.Match.Matches(req.Connector, req.Target) {
			continue
		}
		for j, r := range b.Rules {
			if r.matches(req) {
				add(ruleID(r, fmt.Sprintf("targets[%d].rules[%d]", i, j)), r.Decision, r.Reason, r.Approval)
			}
		}
	}
	// 5. Principal rules.
	for i, b := range e.file.Principals {
		if !b.Match.matches(req.Principal) {
			continue
		}
		for j, r := range b.Rules {
			if r.matches(req) {
				add(ruleID(r, fmt.Sprintf("principals[%d].rules[%d]", i, j)), r.Decision, r.Reason, r.Approval)
			}
		}
	}
	res := combine(matches, e.snapshot, req.DryRun)
	res.Posture = posture
	return res
}

// baselineOverridden reports whether the operator's file replaced this
// baseline cell. An operator's cell is an operator rule, so it applies
// under every posture.
func (e *Evaluator) baselineOverridden(effect contract.Effect, kind string) bool {
	if e.file.Baseline == nil {
		return false
	}
	d, ok := e.file.Baseline.ByEffect[effect][kind]
	return ok && d.Valid()
}

// postureRuleName names what made the evaluation permissive: the global
// posture, or the posture rules that matched.
func postureRuleName(rules []int) string {
	if len(rules) == 0 {
		return "posture.permissive"
	}
	return fmt.Sprintf("posture_rules[%d].permissive", rules[0])
}

// baselineCell is the built-in cell, or the operator's replacement for it.
// The baseline is the one layer a policy file can loosen: every other layer
// only adds restrictions, because the most restrictive match wins.
func (e *Evaluator) baselineCell(effect contract.Effect, kind string) Decision {
	if e.file.Baseline != nil {
		if d, ok := e.file.Baseline.ByEffect[effect][kind]; ok && d.Valid() {
			return d
		}
	}
	if kind == "automation" {
		kind = "agent"
	}
	if d, ok := Baseline[effect][kind]; ok {
		return d
	}
	return Deny
}

// granted reports whether a principal block that matches grants name.
func (e *Evaluator) granted(p Principal, name string) bool {
	for _, b := range e.file.Principals {
		if b.Match.matches(p) && containsFold(b.Grants, name) {
			return true
		}
	}
	return target.DefaultAdhocGrant(p.Kind)
}

func localDevLifecycle(req Request, effect contract.Effect, kind string) bool {
	return effect == contract.EffectLifecycle && kind == "human" && req.Connector == "local" && req.Target.Env == target.EnvDev
}

func isChange(e contract.Effect) bool {
	for _, c := range changes {
		if e == c {
			return true
		}
	}
	return false
}

// principalColumn is the baseline column for a kind: an unknown kind is an
// agent, the strict reading.
func principalColumn(kind string) string {
	switch kind {
	case "human", "agent", "automation":
		return kind
	}
	return "agent"
}

func baselineReason(effect contract.Effect, kind string, d Decision) string {
	switch d {
	case Deny:
		return fmt.Sprintf("the baseline does not let %s run %s operations", article(kind), effect)
	case Approve:
		return fmt.Sprintf("the baseline requires approval for %s's %s operation", article(kind), effect)
	case Allow, DryRunOnly:
	}
	return ""
}

func ruleID(r Rule, path string) string {
	if r.ID != "" {
		return r.ID
	}
	return path
}

func article(kind string) string {
	if strings.IndexAny(kind[:1], "aeiou") == 0 {
		return "an " + kind
	}
	return "a " + kind
}
