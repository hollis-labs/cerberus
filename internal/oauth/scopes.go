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

// The host scopes are narrower than the effect scopes: they cover the host
// registry (connector "host") and nothing else, so a service such as Tachyon
// can read or change hosts without being able to touch a resource, a pipeline
// or a secret.
const (
	// ScopeHostsRead is the read operations of the host connector: hosts,
	// components, status, samples, events, capacity, history.
	ScopeHostsRead = "cerberus:hosts.read"
	// ScopeHostsWrite adds the host registry writes in HostWriteOperations. It
	// does not cover removing a host or unit actions (start, stop, restart,
	// logs), which stay under operate.
	ScopeHostsWrite = "cerberus:hosts.write"
)

// HostWriteOperations are the host connector's operations cerberus:hosts.write
// covers. host_remove and the unit actions are deliberately not in it.
var HostWriteOperations = map[string]bool{
	"host_update": true, "host_inventory": true,
	"component_create": true, "component_update": true, "component_move": true, "component_remove": true,
}

// Supported are the scopes this resource understands.
var Supported = []string{ScopeRead, ScopeReadSensitive, ScopeOperate, ScopeHostsRead, ScopeHostsWrite}

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

// AllowsOp is Allows with the operation known: the effect scopes decide as
// they always did, and the host scopes additionally cover the host connector's
// reads (hosts.read, hosts.write) and its HostWriteOperations (hosts.write).
func AllowsOp(scopes []string, connector, operation string, effect contract.Effect) bool {
	if Allows(scopes, effect) {
		return true
	}
	if connector != "host" {
		return false
	}
	var read, write bool
	for _, s := range scopes {
		read = read || s == ScopeHostsRead || s == ScopeHostsWrite
		write = write || s == ScopeHostsWrite
	}
	switch {
	case effect == contract.EffectRead:
		return read
	case HostWriteOperations[operation]:
		return write
	}
	return false
}

// RequiredScopeOp names what an operation needs, for a refusal: for the host
// connector the narrow host scope, with the effect scope as the alternative.
func RequiredScopeOp(connector, operation string, effect contract.Effect) string {
	if connector == "host" {
		switch {
		case effect == contract.EffectRead:
			return ScopeHostsRead + " (or " + RequiredScope(effect) + ")"
		case HostWriteOperations[operation]:
			return ScopeHostsWrite + " (or " + ScopeOperate + ")"
		}
	}
	return RequiredScope(effect)
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
