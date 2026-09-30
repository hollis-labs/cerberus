package oauth

import (
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Scopes narrow what a token's caller may ask for; they never widen what
// policy allows. An OAuth caller is an agent whatever its scopes.
const (
	// ScopeRead is plain reads.
	ScopeRead = "cerberus:read"
	// ScopeReadSensitive adds reads of text Cerberus did not compose:
	// logs, command output.
	ScopeReadSensitive = "cerberus:read_sensitive"
	// ScopeOperate is any effect, each still decided by policy.
	ScopeOperate = "cerberus:operate"
)

// Supported are the scopes this resource understands.
var Supported = []string{ScopeRead, ScopeReadSensitive, ScopeOperate}

// RequiredScope is the least scope an effect needs.
func RequiredScope(effect contract.Effect) string {
	switch effect {
	case contract.EffectRead:
		return ScopeRead
	case contract.EffectReadSensitive:
		return ScopeReadSensitive
	case contract.EffectWrite, contract.EffectLifecycle, contract.EffectDestructive, contract.EffectExec, contract.EffectAdmin:
		return ScopeOperate
	}
	// An effect nobody classified reads as the strictest.
	return ScopeOperate
}

// Allows reports whether scopes cover effect. Each scope implies the ones
// below it: operate covers read_sensitive, which covers read.
func Allows(scopes []string, effect contract.Effect) bool {
	rank := map[string]int{ScopeRead: 1, ScopeReadSensitive: 2, ScopeOperate: 3}
	best := 0
	for _, s := range scopes {
		if r := rank[s]; r > best {
			best = r
		}
	}
	return best >= rank[RequiredScope(effect)]
}

// ParseScopes normalizes a scope list: known scopes only, deduplicated.
func ParseScopes(in []string) ([]string, []string) {
	known := map[string]bool{}
	for _, s := range Supported {
		known[s] = true
	}
	var out, unknown []string
	seen := map[string]bool{}
	for _, raw := range in {
		for _, s := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' }) {
			switch {
			case seen[s]:
			case known[s]:
				seen[s] = true
				out = append(out, s)
			default:
				unknown = append(unknown, s)
			}
		}
	}
	return out, unknown
}
