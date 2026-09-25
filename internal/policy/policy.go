// Package policy is Cerberus's policy decision point: operator-owned YAML,
// evaluated by one combining rule — the most restrictive match wins
// (docs/plans/live-systems-security-target.md, section 4 and Decision 1).
//
// Layers, general to specific: a built-in baseline by effect class and
// principal kind, the target defaults (Decisions 17 and 18), provider
// profiles, target rules and principal rules. Every rule that matches is
// listed with the decision, so a decision can always be explained.
//
// In P2 the decision point runs in shadow mode: it evaluates every operation
// and its result is recorded, but it changes no outcome. Enforcement arrives
// with approvals in P3.
package policy

import (
	"time"

	contract "github.com/hollis-labs/cerberus/pkg/connector"

	"github.com/hollis-labs/cerberus/internal/target"
)

// Decision is what policy says about an operation.
type Decision string

const (
	Allow      Decision = "allow"
	DryRunOnly Decision = "dry_run_only"
	Approve    Decision = "approve"
	Deny       Decision = "deny"
)

// Rank orders decisions from least to most restrictive: deny > approve >
// dry_run_only > allow. An unknown decision is the most restrictive.
func (d Decision) Rank() int {
	switch d {
	case Allow:
		return 0
	case DryRunOnly:
		return 1
	case Approve:
		return 2
	case Deny:
		return 3
	}
	return 4
}

// Valid reports a decision in the vocabulary.
func (d Decision) Valid() bool { return d.Rank() < 4 }

// Approval says how an approve decision is met (P3). Recorded now; inert.
type Approval struct {
	Channel   string        `yaml:"channel,omitempty" json:"channel,omitempty"`
	Approvers int           `yaml:"approvers,omitempty" json:"approvers,omitempty"`
	Scope     string        `yaml:"scope,omitempty" json:"scope,omitempty"`
	TTL       time.Duration `yaml:"ttl,omitempty" json:"ttl,omitempty"`
}

// Request is one operation, as the decision point sees it.
type Request struct {
	Connector string
	Operation string
	// Effect is the operation's effect class; empty is unknown.
	Effect contract.Effect
	// DryRun is a preview request for an operation that has one.
	DryRun bool
	Target target.Target
	// Principal is the caller's kind, client and session.
	Principal Principal
}

// Principal is who asks, for matching. A label, never approval.
type Principal struct {
	Kind    string
	Client  string
	Session string
}

// Match is one rule that matched, and what it said.
type Match struct {
	Rule     string    `json:"rule"`
	Decision Decision  `json:"decision"`
	Reason   string    `json:"reason,omitempty"`
	Approval *Approval `json:"approval,omitempty"`
}

// Result is the decision and every rule behind it.
type Result struct {
	Decision Decision `json:"decision"`
	Matched  []Match  `json:"matched_rules"`
	// WouldBlock says the operation would not run as asked once policy is
	// enforced: a deny, or an approval nobody has given yet.
	WouldBlock bool `json:"would_block"`
	// Snapshot names the policy that decided: the applied snapshot's hash,
	// "baseline" when none is applied, or "mismatch" when the applied file
	// no longer matches its recorded hash.
	Snapshot string `json:"snapshot"`
}

// Reason is the most restrictive match's reason, for a refusal.
func (r Result) Reason() string {
	for _, m := range r.Matched {
		if m.Decision == r.Decision && m.Reason != "" {
			return m.Reason
		}
	}
	return ""
}

// PDP is the policy decision point. It is deliberately narrow, so another
// evaluator (Cedar) can replace this one behind it (Decision 1).
type PDP interface {
	Authorize(Request) Result
}

// combine applies the one combining rule: the most restrictive match wins.
// No match at all is deny: an operation nothing speaks for is not allowed
// (I2).
func combine(matches []Match, snapshot string, dryRun bool) Result {
	r := Result{Decision: Deny, Matched: matches, Snapshot: snapshot}
	if len(matches) == 0 {
		r.Matched = []Match{{Rule: "builtin.no-match", Decision: Deny, Reason: "no rule covers this operation"}}
		r.WouldBlock = true
		return r
	}
	best := Allow
	for _, m := range matches {
		if m.Decision.Rank() > best.Rank() {
			best = m.Decision
		}
	}
	if !best.Valid() {
		best = Deny
	}
	r.Decision = best
	switch {
	case best == Deny:
		r.WouldBlock = true
	case dryRun:
		// A dry run is the plan step: approval and dry_run_only let it
		// through; only a deny stops it.
	case best == Approve || best == DryRunOnly:
		r.WouldBlock = true
	}
	return r
}
