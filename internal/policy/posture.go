package policy

import (
	"fmt"
	"strings"
)

// PostureSummary is the applied posture as every surface shows it: status,
// whoami, the console header, the MCP instructions (section 13: "it is
// always visible").
type PostureSummary struct {
	// Global is the posture for the host-wide switches, and where target
	// evaluation starts.
	Global string `json:"global"`
	// Rules are the posture rules, in file order.
	Rules []PostureRuleSummary `json:"rules,omitempty"`
	// Snapshot is the applied snapshot's hash, "baseline" or "mismatch"
	// (a mismatch decides as the baseline, which is secure).
	Snapshot string `json:"snapshot"`
}

// PostureRuleSummary is one posture rule: what it matches, and the posture.
type PostureRuleSummary struct {
	Match   string `json:"match"`
	Posture string `json:"posture"`
}

// PostureSummary summarizes f's posture.
func (f File) PostureSummary(snapshot string) PostureSummary {
	s := PostureSummary{Global: f.GlobalPosture(), Snapshot: snapshot}
	for _, r := range f.PostureRules {
		s.Rules = append(s.Rules, PostureRuleSummary{Match: r.Match.String(), Posture: r.Posture})
	}
	return s
}

// Permissive reports whether anything is permissive: the global posture or
// any rule.
func (s PostureSummary) Permissive() bool {
	if s.Global == PosturePermissive {
		return true
	}
	for _, r := range s.Rules {
		if r.Posture == PosturePermissive {
			return true
		}
	}
	return false
}

// String is the one-line form: "secure", "permissive", or the global posture
// followed by the scoped exceptions, e.g.
// "secure; permissive for env=dev and env=lab (labeled targets only)".
func (s PostureSummary) String() string {
	global := s.Global
	if global == "" {
		global = PostureSecure
	}
	var permissive, secure []string
	for _, r := range s.Rules {
		switch r.Posture {
		case PosturePermissive:
			permissive = append(permissive, r.Match)
		case PostureSecure:
			secure = append(secure, r.Match)
		}
	}
	out := global
	if len(permissive) > 0 {
		out += "; permissive for " + strings.Join(permissive, " and ") + " (labeled targets only)"
	}
	if len(secure) > 0 {
		out += "; secure for " + strings.Join(secure, " and ")
	}
	return out
}

// String renders a match as its set fields, e.g. "env=dev owner=self".
func (m TargetMatch) String() string {
	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	add("id", m.ID)
	add("connector", m.Connector)
	add("kind", m.Kind)
	add("env", m.Env)
	add("owner", m.Owner)
	add("admin", m.Admin)
	if len(m.Tags) > 0 {
		add("tags", strings.Join(m.Tags, ","))
	}
	if m.Adhoc != nil {
		add("adhoc", fmt.Sprint(*m.Adhoc))
	}
	if len(parts) == 0 {
		return "every target"
	}
	return strings.Join(parts, " ")
}

// CurrentPosture is the applied posture in s: the snapshot `cerberus policy
// apply` wrote, checked against its hash. Nothing applied, or a snapshot
// that fails its check, is the baseline, which is secure.
func (s Store) CurrentPosture() PostureSummary {
	ev, status := s.Load()
	return ev.File().PostureSummary(status.Snapshot)
}
