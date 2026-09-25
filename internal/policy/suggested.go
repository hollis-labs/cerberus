package policy

import (
	"fmt"
	"sort"

	plugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// FromSuggested is a plugin's suggested policy as the operator's provider
// profile (D7). Accepting a plugin's review writes it as a working file,
// which is inert until `cerberus policy apply`: the plugin's word never
// becomes policy by itself (I10).
//
//	ack                 -> approve, tty_confirm
//	approval            -> approve, out_of_band
//	approval_for_agents -> approve, for agents
//	deny                -> deny
func FromSuggested(connector string, rules []plugin.SuggestedRule) File {
	f := File{Version: FileVersion}
	if len(rules) == 0 {
		return f
	}
	var out []Rule
	for _, s := range rules {
		r := Rule{ID: fmt.Sprintf("%s.suggested.%s", connector, s.Operation), Ops: []string{s.Operation}, Reason: s.Reason}
		switch s.Require {
		case plugin.RequireAck:
			r.Decision, r.Approval = Approve, &Approval{Channel: "tty_confirm"}
		case plugin.RequireApproval:
			r.Decision, r.Approval = Approve, &Approval{Channel: "out_of_band"}
		case plugin.RequireApprovalForAgents:
			r.Decision, r.Principal = Approve, &PrincipalMatch{Kind: "agent"}
		case plugin.RequireDeny:
			r.Decision = Deny
		default:
			continue
		}
		out = append(out, r)
	}
	f.Providers = map[string]Provider{connector: {Rules: out}}
	return f
}

// RuleLines are a file's rules as one line each, for showing a diff.
func RuleLines(f File) []string {
	var out []string
	for id, p := range f.Providers {
		for _, r := range p.Rules {
			line := fmt.Sprintf("%s: %s %v", id, r.Decision, r.Ops)
			if r.Principal != nil && r.Principal.Kind != "" {
				line += " for " + r.Principal.Kind
			}
			if r.Approval != nil && r.Approval.Channel != "" {
				line += " via " + r.Approval.Channel
			}
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}
