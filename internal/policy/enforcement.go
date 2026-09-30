package policy

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Enforcement is the switch-on (P3-7, D6): which decisions are enforced
// rather than recorded in shadow. Mode enforce enforces everything; mode
// shadow (the default) enforces only the calls an enforce entry matches.
// It is part of the snapshot, so it is hashed and changes only through
// `policy apply` and `policy enforce`.
type Enforcement struct {
	Mode    string         `yaml:"mode,omitempty" json:"mode,omitempty"`
	Enforce []EnforceEntry `yaml:"enforce,omitempty" json:"enforce,omitempty"`
}

// Enforcement modes.
const (
	EnforceShadow = "shadow"
	EnforceAll    = "enforce"
)

// EnforceEntry is one enforced scope: the targets it matches, the principal
// kind ("agent", "human", or "!human" for anything else), and the effects.
// An empty field matches everything.
type EnforceEntry struct {
	ID        string            `yaml:"id,omitempty" json:"id,omitempty"`
	Match     TargetMatch       `yaml:"match,omitempty" json:"match,omitempty"`
	Principal string            `yaml:"principal,omitempty" json:"principal,omitempty"`
	Effect    []contract.Effect `yaml:"effect,omitempty" json:"effect,omitempty"`
}

// String is the entry as a person reads it.
func (e EnforceEntry) String() string {
	var parts []string
	if m := e.Match.String(); m != "" {
		parts = append(parts, m)
	}
	if e.Principal != "" {
		parts = append(parts, "principal="+e.Principal)
	}
	if len(e.Effect) > 0 {
		effects := make([]string, len(e.Effect))
		for i, eff := range e.Effect {
			effects[i] = string(eff)
		}
		parts = append(parts, "effect="+strings.Join(effects, ","))
	}
	if len(parts) == 0 {
		parts = append(parts, "everything")
	}
	s := strings.Join(parts, " ")
	if e.ID != "" {
		s = e.ID + ": " + s
	}
	return s
}

// Matches reports whether the entry covers req.
func (e EnforceEntry) Matches(req Request) bool {
	if !e.Match.Matches(req.Connector, req.Target) {
		return false
	}
	if e.Principal != "" {
		want, negate := strings.CutPrefix(e.Principal, "!")
		if (req.Principal.Kind == want) == negate {
			return false
		}
	}
	if len(e.Effect) > 0 {
		found := false
		for _, eff := range e.Effect {
			if eff == req.Effect {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Enforced reports whether req is enforced, and by what.
func (e Enforcement) Enforced(req Request) (bool, string) {
	if e.Mode == EnforceAll {
		return true, "mode: enforce"
	}
	for _, entry := range e.Enforce {
		if entry.Matches(req) {
			return true, entry.String()
		}
	}
	return false, ""
}

// Summary is the enforcement as a person reads it.
func (e Enforcement) Summary() string {
	if e.Mode == EnforceAll {
		return "enforce (everything)"
	}
	if len(e.Enforce) == 0 {
		return "shadow (only the built-in protections are enforced)"
	}
	scopes := make([]string, len(e.Enforce))
	for i, entry := range e.Enforce {
		scopes[i] = entry.String()
	}
	return fmt.Sprintf("shadow, with %d enforced scope(s): %s", len(e.Enforce), strings.Join(scopes, "; "))
}

func (e *Enforcement) problems() []string {
	if e == nil {
		return nil
	}
	var out []string
	if e.Mode != "" && e.Mode != EnforceShadow && e.Mode != EnforceAll {
		out = append(out, fmt.Sprintf("enforcement.mode %q is not shadow or enforce", e.Mode))
	}
	for i, entry := range e.Enforce {
		at := fmt.Sprintf("enforcement.enforce[%d]", i)
		out = append(out, entry.Match.problems(at+".match")...)
		if entry.Principal != "" && !validKind(strings.TrimPrefix(entry.Principal, "!")) {
			out = append(out, fmt.Sprintf("%s.principal %q is not human, agent or automation", at, entry.Principal))
		}
		for _, eff := range entry.Effect {
			if !eff.Valid() {
				out = append(out, fmt.Sprintf("%s.effect %q is not an effect class", at, eff))
			}
		}
	}
	return out
}

// EnforcementOf is the file's enforcement: shadow, with no scopes, when it
// declares none.
func (f File) EnforcementOf() Enforcement {
	if f.Enforcement == nil {
		return Enforcement{Mode: EnforceShadow}
	}
	out := *f.Enforcement
	if out.Mode == "" {
		out.Mode = EnforceShadow
	}
	return out
}

// Record is the enforcement as a snapshot's audit record carries it, so a
// snapshot that later fails its hash check can be enforced as it was last
// verified (LastVerifiedEnforcement).
func (e Enforcement) Record() json.RawMessage {
	data, err := json.Marshal(e)
	if err != nil {
		return nil
	}
	return data
}

// Verified is the snapshot the newest apply the audit log vouches for
// wrote: its hash, its enforcement, and, where the apply recorded it, the
// snapshot itself.
type Verified struct {
	Hash        string
	Enforcement Enforcement
	// Snapshot is the applied file as written, from the apply's outcome;
	// nil for an apply recorded before snapshots were, or still running.
	Snapshot []byte
	Time     time.Time
}

// History is what the audit log says about applies.
type History int

const (
	// NoApplies: the log records no apply, as on a fresh install or after
	// the applies were pruned. The snapshot files are all there is.
	NoApplies History = iota
	// VerifiedApply: an apply the chain vouches for (Verified).
	VerifiedApply
	// UnverifiedApplies: the log records applies, and none of them where
	// the chain vouches for it (past a break, until a reanchor and a new
	// apply). Nothing can be vouched for: fail closed.
	UnverifiedApplies
)

// LastVerified is the newest apply the audit log vouches for (M3): a
// successful outcome the chain trusts (audit.Check), or the trusted intent
// of an apply still running, which recorded its hash before writing the
// files. An apply that failed is passed over, and so is one past a break in
// the chain, however it reads.
func LastVerified(auditDir string) (Verified, History) {
	if auditDir == "" {
		return Verified{}, NoApplies
	}
	checked, err := audit.Check(auditDir)
	if err != nil {
		return Verified{}, UnverifiedApplies
	}
	seen := false
	finished := map[string]bool{}
	for i := len(checked.Records) - 1; i >= 0; i-- {
		r := checked.Records[i]
		if r.Connector != "policy" || r.Operation != "apply" || (r.Kind != audit.KindIntent && r.Kind != audit.KindOutcome) {
			continue
		}
		// Any record that says it is an apply counts as one seen, so one
		// edited out of shape cannot turn the log into "no applies". Only
		// the host's own shape vouches for a snapshot: its target kind too
		// (H-d; a plugin cannot take the id policy).
		seen = true
		if r.Target.Kind != "policy.snapshot" {
			continue
		}
		if r.Kind == audit.KindOutcome {
			if r.OperationID != "" {
				finished[r.OperationID] = true
			}
			if !checked.Trusted[i] || r.Decision != audit.DecisionAllowed || (r.OutcomeCode != "" && r.OutcomeCode != audit.OutcomeOK) {
				continue
			}
		} else if finished[r.OperationID] || !checked.Trusted[i] {
			continue
		}
		v := Verified{Hash: r.Target.Fields["hash"], Enforcement: Enforcement{Mode: EnforceShadow}, Snapshot: []byte(r.PolicySnapshot), Time: r.Time}
		if len(r.Enforcement) > 0 && json.Unmarshal(r.Enforcement, &v.Enforcement) != nil {
			continue
		}
		return v, VerifiedApply
	}
	if seen {
		return Verified{}, UnverifiedApplies
	}
	return Verified{}, NoApplies
}

// BaselineEnforcedSummary is what is always enforced, as a person reads it.
const BaselineEnforcedSummary = "an agent's write, lifecycle, destructive or exec operation on a target labeled env: prod or admin: owner"

// BaselineEnforced reports whether req is enforced whatever the snapshot's
// enforcement says (B2): a change by an agent, or by a caller Cerberus
// cannot place, to a target labeled production or administered by its
// owner. Shadow stays the default for the policy an operator writes; these
// built-in protections are not something an agent's own acknowledgment
// should be able to walk past on a new install. A person's call, and
// automation acting on its own or for a person, are not covered, and a
// target with no such label is not either: an unlabeled local resource is
// not read as production here.
func BaselineEnforced(req Request) bool {
	switch req.Principal.Kind {
	case "human":
		return false
	case "automation":
		// Automation is the caller it acts for: the monitor, or a person's
		// run, is not covered; a run an agent started is that agent.
		if a := req.Principal.ActingFor; a == "" || a == "human" {
			return false
		}
	}
	if !isChange(effectOf(req)) {
		return false
	}
	t := req.Target
	return t.Env == target.EnvProd || t.AdminFor == target.AdminOwner
}
