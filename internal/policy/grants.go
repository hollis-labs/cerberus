package policy

import (
	"fmt"
	"sort"
	"strings"
)

// Grant scopes an approve rule's approval may ask for (P3-5).
const (
	ScopeOnce    = "once"
	ScopeSession = "session"
	ScopeWindow  = "window"
)

// IsGrant reports an approval that asks for a session or window grant:
// reusable until its TTL, where a once approval is spent by one call.
func (a *Approval) IsGrant() bool {
	return a != nil && (a.Scope == ScopeSession || a.Scope == ScopeWindow)
}

// GrantWarnings are the rules that allow a session or window grant which
// could apply to a prod, shared or not-ours target (P3-5, D5). Grants are
// the operator's choice anywhere policy allows them; this is the loud
// part, shown by `policy apply` and `policy explain`. A rule is safe only
// when its target match pins env to dev or lab, owner to self and admin to
// something other than shared; a provider or principal rule matches every
// target, so it never is.
func (f File) GrantWarnings() []string {
	var out []string
	check := func(where string, rules []Rule, safe bool) {
		for i, r := range rules {
			if r.Decision != Approve || !r.Approval.IsGrant() || safe {
				continue
			}
			at := fmt.Sprintf("%s.rules[%d]", where, i)
			if r.ID != "" {
				at += " (" + r.ID + ")"
			}
			out = append(out, fmt.Sprintf("%s allows a %s grant that could apply to a prod, shared or not-ours target: once given, it covers every such call for its TTL", at, r.Approval.Scope))
		}
	}
	ids := make([]string, 0, len(f.Providers))
	for id := range f.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		check("providers."+id, f.Providers[id].Rules, false)
	}
	for i, t := range f.Targets {
		check(fmt.Sprintf("targets[%d]", i), t.Rules, t.Match.unprotected())
	}
	for i, p := range f.Principals {
		check(fmt.Sprintf("principals[%d]", i), p.Rules, false)
	}
	return out
}

// unprotected reports a target match that can only select dev or lab
// targets that are ours and not shared.
func (m TargetMatch) unprotected() bool {
	env := strings.ToLower(m.Env)
	admin := strings.ToLower(m.Admin)
	return (env == "dev" || env == "lab") && strings.EqualFold(m.Owner, "self") &&
		admin != "" && !strings.HasPrefix(admin, "!") && admin != "shared"
}
