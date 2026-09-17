// Package registry implements Cerberus's opt-in resource registry.
//
// Apps own a project-config file in their own repo (conventionally
// `<name>.cerberus.yaml`) and register it with Cerberus, which keeps
// only a pointer index at ~/.cerberus/registry.yaml. Resolution is
// local-first: config bodies are always read fresh from the owning
// app's file and are never copied into the index. The index records a
// path handle, not content.
//
// There are two file kinds:
//
//   - ProjectConfig (cerberus-project/v1) — the unit: one project plus
//     its resources, owned by one app. This is what gets registered.
//   - Bundle (cerberus-bundle/v1) — a manifest listing paths to several
//     project configs, so a multi-app repo can register them in one
//     command. Discovery sugar only; it holds no definitions itself.
//
// This package owns both kind contracts and their validators.
package registry

import (
	"fmt"
	"os"

	"github.com/hollis-labs/cerberus/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	// ProjectConfigKind is the kind contract for an app-owned project
	// config. The "/v1" suffix is the schema version; bumping it is a
	// breaking change to the app-owned format.
	ProjectConfigKind = "cerberus-project/v1"

	// DefaultNamespace is assigned to configs that omit `namespace`.
	// The field is reserved from day one for future multi-tenant trust
	// boundaries — every kind carries owner + namespace even though v1
	// only ever resolves the local namespace.
	DefaultNamespace = "local"

	// FileSuffix is the conventional suffix for every Cerberus config
	// file. The basename is free (e.g. torque.cerberus.yaml) so apps can
	// name a config after the project it describes.
	FileSuffix = ".cerberus.yaml"
)

// ProjectConfig is the app-owned config unit and the thing that gets
// registered: exactly one project and its 1..n resources, owned by one
// app. Apps commit this file to their own repo; Cerberus stores only a
// path pointer to it, keyed by Owner.
//
// The body reuses config.ProjectDef / ResourceDef / PipelineDef so the
// resolver can merge configs into a *config.ConfigV2 without a parallel
// type hierarchy.
type ProjectConfig struct {
	// Kind must equal ProjectConfigKind.
	Kind string `yaml:"kind"`

	// Owner identifies the registering app. It is the registry key:
	// unique across all registered configs and stable across the app's
	// lifetime. Lowercase kebab-case.
	//
	// Owner and Project.ID are the same slug. Omit Owner and it defaults
	// from Project.ID at load; write both and they must match.
	Owner string `yaml:"owner,omitempty"`

	// Namespace is reserved for multi-tenant trust isolation. Defaults
	// to DefaultNamespace when omitted.
	Namespace string `yaml:"namespace,omitempty"`

	// RegistryURN is the optional shared-directory identity written back
	// by Tether's cross-substrate registry bootstrap. Cerberus treats it
	// as metadata only: local runtime ownership remains with the app-owned
	// config and Cerberus's local pointer registry.
	RegistryURN string `yaml:"registry_urn,omitempty"`

	// Project is the single project this config contributes. Multi-
	// project repos register multiple project configs (optionally via a
	// Bundle manifest) rather than packing several into one file.
	Project config.ProjectDef `yaml:"project"`

	// Resources and Pipelines carry the definitions, identical in shape
	// to the v2 monolithic config.
	Resources []config.ResourceDef `yaml:"resources,omitempty"`
	Pipelines []config.PipelineDef `yaml:"pipelines,omitempty"`

	// UnknownFields names every field the lenient parse ignored, in
	// yaml.v3's "line N: field x not found in type T" form. It is not
	// itself a YAML field — LoadProjectConfig fills it from a second,
	// strict pass. ValidateProjectConfig turns each into a warning, so
	// the runtime resolver keeps the project; register and validate
	// reject on them, so a typo is still caught at author time.
	UnknownFields []string `yaml:"-"`
}

// LoadProjectConfig reads and parses an app-owned project config.
//
// The parse is deliberately lenient: an unrecognised field is recorded
// in UnknownFields rather than failing the load. Strict parsing here
// was a silent-outage generator — the resolver drops any config that
// fails to load, so one field written by a newer writer took 17 of 18
// projects offline on 2026-05-25 with no error anywhere. Forward
// compatibility at runtime is worth more than typo detection at runtime,
// and nothing is lost: register and `cerberus validate` still reject
// unrecognised fields, which is where a typo is actually introduced.
// Connector-specific resource Config maps stay open by design.
//
// LoadProjectConfig does not validate semantics; call
// ValidateProjectConfig for that.
func LoadProjectConfig(path string) (*ProjectConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("read project config %s: %w", path, err)
	}

	var pc ProjectConfig
	if err := yaml.Unmarshal(data, &pc); err != nil {
		return nil, fmt.Errorf("parse project config %s: %w", path, err)
	}

	if pc.Namespace == "" {
		pc.Namespace = DefaultNamespace
	}
	// owner and project.id are the same slug, so a config need only
	// write it once. Defaulting here rather than requiring both removes
	// the duplication without changing anything that works today: an
	// omitted owner used to be a hard validation error, and when both
	// are written ValidateProjectConfig still requires them to match.
	if pc.Owner == "" {
		pc.Owner = pc.Project.ID
	}
	pc.UnknownFields = unknownFields(data, new(ProjectConfig))
	return &pc, nil
}

// PeekKind reads only the `kind` field of a Cerberus config file so the
// register command can dispatch project-config vs bundle-manifest
// without a full, kind-specific parse.
func PeekKind(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var probe struct {
		Kind string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return "", fmt.Errorf("parse kind of %s: %w", path, err)
	}
	return probe.Kind, nil
}
