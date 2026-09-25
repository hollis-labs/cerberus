// Package target is what an operation acts on, labeled for policy: which
// environment it is in, who owns it, who administers it, and its tags
// (docs/plans/live-systems-security-target.md, section 2 and Decisions 17
// and 18).
//
// A target is resolved from a registered resource where the operation names
// one, and inherits that resource's labels: a namespace inherits from its
// cluster, a record from its zone, a container from its host. Anything left
// unlabeled is unknown, and unknown is read as strictly as possible
// (Decision 17). A target the operation names by connection settings rather
// than by a registered resource is ad hoc; policy grants those separately
// (adhoc_targets), and agents do not have that grant.
//
// Labels choose policy. They are the operator's declarations about their own
// estate, not facts Cerberus verified.
package target

import (
	"fmt"
	"sort"
	"strings"
)

// Env is where a target runs.
type Env string

const (
	EnvProd    Env = "prod"
	EnvStaging Env = "staging"
	EnvDev     Env = "dev"
	EnvLab     Env = "lab"
	EnvPOC     Env = "poc"
	EnvWork    Env = "work"
	EnvUnknown Env = "unknown"
)

// Envs is the vocabulary.
var Envs = []Env{EnvProd, EnvStaging, EnvDev, EnvLab, EnvPOC, EnvWork, EnvUnknown}

// Owner values. An owner is "self" or a team's name.
const (
	OwnerSelf    = "self"
	OwnerUnknown = "unknown"
)

// Admin values: who administers a target day to day (Decision 18).
const (
	// AdminSelf: we administer it.
	AdminSelf = "self"
	// AdminShared: we and the owner both do.
	AdminShared = "shared"
	// AdminOwner: the owner does; we do not change it on our own say-so.
	AdminOwner = "owner"
	// AdminUnknown: nobody said.
	AdminUnknown = "unknown"
)

var adminValues = []string{AdminSelf, AdminShared, AdminOwner}

// invalidAdmin marks an admin that did not parse.
const invalidAdmin = "<invalid>"

// Admin is who administers a target, scopable per sub-target kind: the team
// that provisions a box is often not the team that runs what is on it.
//
//	admin: owner                                            # everything
//	admin: { default: owner, docker: self, software: self } # per kind
//
// A kind key matches an operation's target kind exactly, or its connector
// id, so `docker: self` covers every docker operation on the host.
type Admin struct {
	Default string            `json:"default,omitempty" yaml:"default,omitempty"`
	ByKind  map[string]string `json:"by_kind,omitempty" yaml:"-"`
}

// UnmarshalYAML accepts a bare value or a map with default and kind keys.
func (a *Admin) UnmarshalYAML(unmarshal func(any) error) error {
	var scalar string
	if unmarshal(&scalar) == nil {
		*a = Admin{Default: scalar}
		return nil
	}
	var m map[string]string
	if unmarshal(&m) == nil {
		out := Admin{Default: m["default"]}
		for k, v := range m {
			if k == "default" {
				continue
			}
			if out.ByKind == nil {
				out.ByKind = map[string]string{}
			}
			out.ByKind[k] = v
		}
		*a = out
		return nil
	}
	// A shape nobody could mean loads rather than failing the whole config;
	// Validate reports it and it reads as unknown.
	*a = Admin{Default: invalidAdmin}
	return nil
}

// MarshalYAML writes the bare form when there are no kind keys.
func (a Admin) MarshalYAML() (any, error) {
	if len(a.ByKind) == 0 {
		if a.Default == "" {
			return nil, nil
		}
		return a.Default, nil
	}
	m := map[string]string{}
	for k, v := range a.ByKind {
		m[k] = v
	}
	if a.Default != "" {
		m["default"] = a.Default
	}
	return m, nil
}

// IsZero reports an undeclared admin.
func (a Admin) IsZero() bool { return a.Default == "" && len(a.ByKind) == 0 }

// For is who administers the part of the target an operation of this kind
// and connector touches: the kind's own key, then the connector's, then the
// default, then unknown.
func (a Admin) For(kind, connector string) string {
	if v, ok := a.ByKind[kind]; ok && v != "" {
		return v
	}
	if v, ok := a.ByKind[connector]; ok && v != "" {
		return v
	}
	if a.Default != "" {
		return a.Default
	}
	return AdminUnknown
}

// String is the admin as it reads in a list.
func (a Admin) String() string {
	if len(a.ByKind) == 0 {
		if a.Default == "" {
			return ""
		}
		return a.Default
	}
	keys := make([]string, 0, len(a.ByKind))
	for k := range a.ByKind {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)+1)
	if a.Default != "" {
		parts = append(parts, "default="+a.Default)
	}
	for _, k := range keys {
		parts = append(parts, k+"="+a.ByKind[k])
	}
	return strings.Join(parts, ",")
}

// Labels are what the operator declares about a target.
type Labels struct {
	Env   Env      `json:"env,omitempty" yaml:"env,omitempty"`
	Owner string   `json:"owner,omitempty" yaml:"owner,omitempty"`
	Admin Admin    `json:"admin,omitzero" yaml:"admin,omitempty"`
	Tags  []string `json:"tags,omitempty" yaml:"tags,omitempty"`
}

// Resolved fills what was left out with unknown, the strict reading.
func (l Labels) Resolved() Labels {
	if l.Env == "" {
		l.Env = EnvUnknown
	}
	if l.Owner == "" {
		l.Owner = OwnerUnknown
	}
	return l
}

// Unlabeled reports a target with neither env nor owner declared: the
// audit log lists these as needing a label before policy is enforced.
func (l Labels) Unlabeled() bool {
	return (l.Env == "" || l.Env == EnvUnknown) && (l.Owner == "" || l.Owner == OwnerUnknown)
}

// Validate reports every label value outside the vocabulary.
func (l Labels) Validate() []string {
	var problems []string
	if l.Env != "" && !validEnv(l.Env) {
		problems = append(problems, fmt.Sprintf("env %q is not one of %s", l.Env, joinEnvs()))
	}
	if strings.TrimSpace(l.Owner) != l.Owner {
		problems = append(problems, fmt.Sprintf("owner %q has surrounding space", l.Owner))
	}
	switch {
	case l.Admin.Default == invalidAdmin:
		problems = append(problems, fmt.Sprintf("admin must be %s, or a map of those by sub-target kind with an optional default", strings.Join(adminValues, ", ")))
	case l.Admin.Default != "" && !validAdmin(l.Admin.Default):
		problems = append(problems, fmt.Sprintf("admin %q is not one of %s", l.Admin.Default, strings.Join(adminValues, ", ")))
	}
	keys := make([]string, 0, len(l.Admin.ByKind))
	for k := range l.Admin.ByKind {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !validAdmin(l.Admin.ByKind[k]) {
			problems = append(problems, fmt.Sprintf("admin.%s %q is not one of %s", k, l.Admin.ByKind[k], strings.Join(adminValues, ", ")))
		}
	}
	return problems
}

// Sanitized is the labels with every value outside the vocabulary dropped,
// so it reads as unknown. The strict reading, never a guess at what was
// meant (Decision 17).
func (l Labels) Sanitized() Labels {
	if l.Env != "" && !validEnv(l.Env) {
		l.Env = ""
	}
	l.Owner = strings.TrimSpace(l.Owner)
	if l.Admin.Default != "" && !validAdmin(l.Admin.Default) {
		l.Admin.Default = ""
	}
	if len(l.Admin.ByKind) > 0 {
		kept := map[string]string{}
		for k, v := range l.Admin.ByKind {
			if validAdmin(v) {
				kept[k] = v
			}
		}
		l.Admin.ByKind = kept
	}
	return l
}

// Target is an operation's target, labeled.
type Target struct {
	Connector string `json:"connector"`
	Kind      string `json:"kind,omitempty"`
	// ID names the target: the resource id, or the operation's first
	// identifying field.
	ID string `json:"id,omitempty"`
	// Resource is the registered resource the target was resolved through,
	// and whose labels it inherits. Empty for a target that is not one.
	Resource string `json:"resource,omitempty"`
	// Labels are the resource's, with unknown for anything undeclared.
	Labels
	// AdminFor is who administers the part this operation touches.
	AdminFor string `json:"admin_for"`
	// Adhoc is a target named by connection settings (a host, a context, a
	// compose file) rather than a registered resource.
	Adhoc bool `json:"adhoc,omitempty"`
}

// Resolve labels an operation's target. res is the registered resource the
// operation named, or nil; adhoc says the operation named its target by
// connection settings instead.
func Resolve(connector, kind, id string, res *ResourceLabels, adhoc bool) Target {
	t := Target{Connector: connector, Kind: kind, ID: id, Adhoc: adhoc}
	var labels Labels
	if res != nil {
		t.Resource = res.ID
		if t.ID == "" {
			t.ID = res.ID
		}
		labels = res.Labels
	}
	t.Labels = labels.Resolved()
	t.AdminFor = labels.Admin.For(kind, connector)
	return t
}

// ResourceLabels are a registered resource's id and labels.
type ResourceLabels struct {
	ID string
	Labels
}

func validEnv(e Env) bool {
	for _, known := range Envs {
		if e == known {
			return true
		}
	}
	return false
}

func validAdmin(v string) bool {
	for _, known := range adminValues {
		if v == known {
			return true
		}
	}
	return false
}

func joinEnvs() string {
	names := make([]string, len(Envs))
	for i, e := range Envs {
		names[i] = string(e)
	}
	return strings.Join(names, ", ")
}

// AdhocTargets is the grant a free-form target needs: connection settings in
// place of a registered resource. Policy decides who holds it (P2-4, in
// shadow mode until P3 enforces). DefaultAdhocGrant is its default: a human
// in their own shell holds it, and an agent and automation do not. A caller
// kind outside the vocabulary does not.
const AdhocTargets = "adhoc_targets"

// DefaultAdhocGrant reports whether a principal of this kind holds the
// adhoc_targets grant by default.
func DefaultAdhocGrant(kind string) bool {
	return kind == "human"
}
