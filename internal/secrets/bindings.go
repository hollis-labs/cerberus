package secrets

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/secretref"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Read and write credential bindings (I9). A connector's entry in
// connector-secrets.yaml may bind a key separately for reads and for
// writes, at the connector level and per target, so a policy bug meets a
// credential that cannot write:
//
//	cloudflare:
//	  api_token: keychain://cloudflare/api_token       # both accesses, as before
//	  read:  { api_token: keychain://cloudflare/ro }
//	  write: { api_token: keychain://cloudflare/rw }
//	  targets:
//	    - match: { env: prod }
//	      read:  { api_token: op://Prod/cf-ro/token }
//	      write: { api_token: null }                   # no write credential on prod
//
// Resolution for a key takes the first target entry that binds it, then
// the connector level, then the legacy chain (environment, the flat key,
// the keychain). The most specific level that mentions a key decides it,
// for both accesses: a target's `write: null` refuses the write even
// though the connector level has one. There is never a fallback from one
// access to the other.

// Access is which credential a call uses.
type Access string

// Accesses.
const (
	AccessRead  Access = "read"
	AccessWrite Access = "write"
)

// AccessFor is the access an operation's effect needs. Reads and
// read_sensitive read; everything else, and anything unclassified, writes.
// A dry run or a plan request reads: a preview must not need to write.
func AccessFor(effect contract.Effect, preview bool) Access {
	if preview || effect == contract.EffectRead || effect == contract.EffectReadSensitive {
		return AccessRead
	}
	return AccessWrite
}

// CredentialScope is the call a credential is resolved for.
type CredentialScope struct {
	Access Access
	// Target is the call's resolved target, for target-level bindings; a
	// zero target matches no target entry.
	Target target.Target
}

type scopeKey struct{}

// WithCredentialScope marks ctx with the call its credentials are for.
func WithCredentialScope(ctx context.Context, s CredentialScope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// CredentialScopeFrom is ctx's credential scope, if any. Without one,
// resolution is the legacy chain for both accesses.
func CredentialScopeFrom(ctx context.Context) (CredentialScope, bool) {
	s, ok := ctx.Value(scopeKey{}).(CredentialScope)
	return s, ok
}

// Reserved names in a connector's entry.
const (
	keyRead    = "read"
	keyWrite   = "write"
	keyTargets = "targets"
)

// accessMap binds keys for one access; a nil value is an explicit none.
type accessMap map[string]*string

type targetBinding struct {
	Match policy.TargetMatch
	Read  accessMap
	Write accessMap
}

// ConnectorBindings are one connector's entry.
type ConnectorBindings struct {
	Flat    map[string]string
	Read    accessMap
	Write   accessMap
	Targets []targetBinding
}

// Split reports whether any key is bound per access.
func (c ConnectorBindings) Split() bool {
	return len(c.Read) > 0 || len(c.Write) > 0 || len(c.Targets) > 0
}

// BindingFile is connector-secrets.yaml.
type BindingFile map[string]ConnectorBindings

// ErrNoCredential is a binding that says there is no credential for this
// access here: the call is refused, never retried with another.
var ErrNoCredential = errors.New("no credential is bound for this access")

// ParseBindings reads connector-secrets.yaml. isRef says whether a value
// is a reference; the file holds references only.
func ParseBindings(data []byte, path string, isRef func(string) bool) (BindingFile, error) {
	var raw map[string]map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid connector secret reference mapping in %s: %w", path, err)
	}
	out := BindingFile{}
	var problems []string
	for connector, entries := range raw {
		c := ConnectorBindings{Flat: map[string]string{}}
		for name, node := range entries {
			at := connector + "." + name
			switch name {
			case keyRead, keyWrite:
				m, p := parseAccessMap(at, &node, isRef)
				problems = append(problems, p...)
				if name == keyRead {
					c.Read = m
				} else {
					c.Write = m
				}
			case keyTargets:
				var list []struct {
					Match policy.TargetMatch `yaml:"match"`
					Read  yaml.Node          `yaml:"read"`
					Write yaml.Node          `yaml:"write"`
				}
				if err := node.Decode(&list); err != nil {
					problems = append(problems, fmt.Sprintf("%s: expected a list of {match, read, write}: %v", at, err))
					continue
				}
				for i, t := range list {
					tat := fmt.Sprintf("%s[%d]", at, i)
					if t.Match.String() == "every target" {
						problems = append(problems, tat+".match: name what the entry is for (env, id, owner, …); an empty match would cover every target")
					}
					r, p1 := parseAccessMap(tat+".read", &t.Read, isRef)
					w, p2 := parseAccessMap(tat+".write", &t.Write, isRef)
					problems = append(problems, p1...)
					problems = append(problems, p2...)
					problems = append(problems, bothHalves(tat, r, w)...)
					c.Targets = append(c.Targets, targetBinding{Match: t.Match, Read: r, Write: w})
				}
			default:
				var ref string
				if node.Kind != yaml.ScalarNode || node.Decode(&ref) != nil {
					problems = append(problems, fmt.Sprintf("%s: expected a reference; read, write and targets are the only nested keys", at))
					continue
				}
				if !isRef(ref) {
					problems = append(problems, fmt.Sprintf("connector secret %s/%s must be a secret reference (%s://); literal credentials are not allowed", connector, name, strings.Join(secretref.Schemes(), "://, ")))
					continue
				}
				c.Flat[name] = ref
			}
		}
		problems = append(problems, bothHalves(connector, c.Read, c.Write)...)
		out[connector] = c
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("invalid connector secret reference mapping in %s: %s", path, strings.Join(problems, "; "))
	}
	return out, nil
}

func parseAccessMap(at string, node *yaml.Node, isRef func(string) bool) (accessMap, []string) {
	if node == nil || node.Kind == 0 {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, []string{at + ": expected key: reference (or null for none)"}
	}
	out := accessMap{}
	var problems []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		if val.Tag == "!!null" {
			out[key] = nil
			continue
		}
		var ref string
		if val.Kind != yaml.ScalarNode || val.Decode(&ref) != nil || !isRef(ref) {
			problems = append(problems, fmt.Sprintf("%s.%s must be a secret reference, or null for none; literal credentials are not allowed", at, key))
			continue
		}
		out[key] = &ref
	}
	return out, problems
}

// bothHalves: a key bound for one access at a level is bound (or nulled)
// for the other there too, so what a write uses is never an accident of
// what was left out.
func bothHalves(at string, read, write accessMap) []string {
	var problems []string
	for key := range read {
		if _, ok := write[key]; !ok {
			problems = append(problems, fmt.Sprintf("%s binds %s for read and not for write; bind it for write too, or write: {%s: null} for none", at, key, key))
		}
	}
	for key := range write {
		if _, ok := read[key]; !ok {
			problems = append(problems, fmt.Sprintf("%s binds %s for write and not for read; bind it for read too, or read: {%s: null} for none", at, key, key))
		}
	}
	return problems
}

// Binding is what a key resolves to for one call.
type Binding struct {
	// Ref is the reference to resolve; empty means the legacy chain.
	Ref string
	// None is an explicit null: no credential for this access here.
	None bool
	// Label names the binding for records and refusals:
	// "targets[0].write", "write", or "" for the legacy chain.
	Label string
}

// Resolve is the binding for key under scope. With no scope, or a key no
// level binds per access, it is the legacy chain (Ref "" unless the flat
// key names one).
func (c ConnectorBindings) Resolve(connector, key string, scope *CredentialScope) Binding {
	if scope != nil {
		pick := func(label string, read, write accessMap) (Binding, bool) {
			m := read
			if scope.Access == AccessWrite {
				m = write
			}
			_, inRead := read[key]
			_, inWrite := write[key]
			if !inRead && !inWrite {
				return Binding{}, false
			}
			label += "." + string(scope.Access)
			ref, ok := m[key]
			if !ok || ref == nil {
				return Binding{None: true, Label: strings.TrimPrefix(label, ".")}, true
			}
			return Binding{Ref: *ref, Label: strings.TrimPrefix(label, ".")}, true
		}
		if scope.Target.Kind != "" || scope.Target.ID != "" {
			for i, t := range c.Targets {
				if !t.Match.Matches(connector, scope.Target) {
					continue
				}
				if b, ok := pick(fmt.Sprintf("targets[%d]", i), t.Read, t.Write); ok {
					return b
				}
			}
		}
		if b, ok := pick("", c.Read, c.Write); ok {
			return b
		}
	}
	return Binding{Ref: c.Flat[key]}
}

// CredentialName is a declared secret's name in a record: the connector,
// the key and, for a per-access binding, which one.
func CredentialName(connector, key string, b Binding) string {
	name := connector + "/" + key
	if b.Label != "" {
		name += "@" + b.Label
	}
	return name
}
