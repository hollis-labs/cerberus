package policy

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
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
		return "shadow (nothing enforced)"
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

// LastVerifiedEnforcement is the enforcement the newest successful snapshot
// apply recorded in the audit log, provided the log's hash chain verifies.
// An apply recorded before enforcement existed recorded none, and was
// shadow. It reports false when the chain does not verify or no apply is
// recorded: then the policy and the record of it have both been tampered
// with, and the caller fails closed.
func LastVerifiedEnforcement(auditDir string) (Enforcement, time.Time, bool) {
	if auditDir == "" || audit.Verify(auditDir) != nil {
		return Enforcement{}, time.Time{}, false
	}
	records, err := audit.ReadRecords(auditDir)
	if err != nil {
		return Enforcement{}, time.Time{}, false
	}
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if r.Kind != audit.KindOutcome || r.Connector != "policy" || r.Operation != "apply" || r.Decision != audit.DecisionAllowed || (r.OutcomeCode != "" && r.OutcomeCode != "ok") {
			continue
		}
		e := Enforcement{Mode: EnforceShadow}
		if len(r.Enforcement) > 0 {
			if err := json.Unmarshal(r.Enforcement, &e); err != nil {
				return Enforcement{}, time.Time{}, false
			}
		}
		return e, r.Time, true
	}
	return Enforcement{}, time.Time{}, false
}
