package registry

import (
	"fmt"
	"regexp"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
)

// Severity classifies a validation issue. Errors block registration and
// resolution; warnings are surfaced but non-fatal.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// identPattern constrains owner and namespace values. They are used as
// registry keys and may appear in paths, so they are kept to lowercase
// kebab-case with no leading separator.
var identPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// slugPattern constrains project.id — the portfolio-wide project slug.
//
// Stricter than identPattern on purpose. identPattern admits a trailing
// or doubled hyphen ("app-", "a--b"); those are harmless as a local
// registry key but this value is also a Tesseract namespace segment and
// an agent-setup template basename, and a slug that round-trips
// differently through those is a join that silently misses. Single
// interior hyphens only.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidationIssue is a single finding from a validator. Field is a
// dotted path into the file (e.g. "resources[clockwork-api].type") to
// make the report actionable.
type ValidationIssue struct {
	Severity string
	Field    string
	Message  string
}

func (i ValidationIssue) String() string {
	return fmt.Sprintf("%s: %s — %s", i.Severity, i.Field, i.Message)
}

// ValidationResult collects every issue found in one file.
type ValidationResult struct {
	Issues []ValidationIssue
}

// HasErrors reports whether any issue is error severity. A result with
// only warnings is still considered valid for registration.
func (r ValidationResult) HasErrors() bool {
	for _, issue := range r.Issues {
		if issue.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Errors returns just the error-severity issues.
func (r ValidationResult) Errors() []ValidationIssue { return r.filter(SeverityError) }

// Warnings returns just the warning-severity issues.
func (r ValidationResult) Warnings() []ValidationIssue { return r.filter(SeverityWarning) }

// UnknownFieldIssues returns the unrecognised-field findings.
//
// These are warnings, not errors, and that asymmetry is deliberate: the
// runtime resolver skips any config whose validation has errors, so
// promoting them would reintroduce the silent whole-project drop this
// leniency exists to prevent. Author-time callers — register and
// `cerberus validate` — select on this and reject, which is where a
// typo can still be caught without a running daemon losing a project.
func (r ValidationResult) UnknownFieldIssues() []ValidationIssue {
	var out []ValidationIssue
	for _, issue := range r.Issues {
		if issue.Field == unknownFieldName {
			out = append(out, issue)
		}
	}
	return out
}

func (r ValidationResult) filter(severity string) []ValidationIssue {
	var out []ValidationIssue
	for _, issue := range r.Issues {
		if issue.Severity == severity {
			out = append(out, issue)
		}
	}
	return out
}

// issueSink accumulates issues; passed to the per-section validators.
type issueSink struct{ result *ValidationResult }

func (s issueSink) add(severity, field, msg string) {
	s.result.Issues = append(s.result.Issues, ValidationIssue{Severity: severity, Field: field, Message: msg})
}

// ValidateProjectConfig checks a project config against the
// cerberus-project/v1 kind contract: the registration envelope
// (kind/owner/namespace), structural integrity (project + resource ids
// present and unique, required resource fields), and the hard runtime
// invariants — most importantly the `port: 0` ban, which has caused
// repeated false-positive "running" incidents and is now schema-
// enforced.
//
// Cross-config references (a resource pointing at a project or
// dependency owned by a different config) are reported as warnings, not
// errors: the resolver, which sees every registered config, is the
// authority on cross-config linkage.
func ValidateProjectConfig(pc *ProjectConfig) ValidationResult {
	var result ValidationResult
	sink := issueSink{result: &result}
	add := sink.add

	if pc == nil {
		add(SeverityError, "project-config", "config is nil")
		return result
	}

	for _, f := range pc.UnknownFields {
		add(SeverityWarning, unknownFieldName, f)
	}

	// --- registration envelope ---
	switch pc.Kind {
	case ProjectConfigKind:
		// ok
	case "":
		add(SeverityError, "kind", fmt.Sprintf("missing; expected %q", ProjectConfigKind))
	default:
		add(SeverityError, "kind", fmt.Sprintf("unsupported kind %q; expected %q", pc.Kind, ProjectConfigKind))
	}

	if pc.Owner == "" {
		add(SeverityError, "owner", "missing; owner is the registry key and is required")
	} else if !identPattern.MatchString(pc.Owner) {
		add(SeverityError, "owner", fmt.Sprintf("invalid value %q; must be lowercase kebab-case", pc.Owner))
	}
	if pc.Namespace != "" && !identPattern.MatchString(pc.Namespace) {
		add(SeverityError, "namespace", fmt.Sprintf("invalid value %q; must be lowercase kebab-case", pc.Namespace))
	}

	// --- project ---
	//
	// project.id is the portfolio-wide slug, not merely a local label:
	// Cerberus keys the registry on it, Tesseract uses it as a memory
	// namespace segment, and agent-setup names project templates after
	// it. Validating it here is the only place that invariant is
	// enforced before those systems join on it.
	switch {
	case pc.Project.ID == "":
		add(SeverityError, "project.id", "missing; a project config must declare exactly one project")
	case !slugPattern.MatchString(pc.Project.ID):
		add(SeverityError, "project.id", fmt.Sprintf(
			"invalid slug %q; must be lowercase kebab-case (letters, digits, single interior hyphens)", pc.Project.ID))
	case pc.Owner != "" && pc.Owner != pc.Project.ID:
		// Two names for one project is a join waiting to pick the wrong
		// one. They have never diverged in practice; this keeps it that
		// way rather than deciding later which one other systems meant.
		add(SeverityError, "project.id", fmt.Sprintf(
			"is %q but owner is %q; they name the same project and must match", pc.Project.ID, pc.Owner))
	}

	for i, link := range pc.Project.Links {
		field := fmt.Sprintf("project.links[%d]", i)
		// kind stays free-form (Tether ADR 0041 D16) — validated as
		// present, never against a closed vocabulary.
		if link.Kind == "" {
			add(SeverityError, field+".kind", "missing; a link must say what kind of relation it is")
		}
		if link.Target == "" {
			add(SeverityError, field+".target", "missing; a link must point at something")
		}
	}

	// --- resources ---
	if len(pc.Resources) == 0 {
		add(SeverityWarning, "resources", "config declares no resources")
	}
	resourceIDs := map[string]bool{}
	for _, resource := range pc.Resources {
		ref := resource.ID
		if ref == "" {
			ref = "<missing-id>"
		}
		field := fmt.Sprintf("resources[%s]", ref)

		if resource.ID == "" {
			add(SeverityError, field+".id", "missing")
		} else if resourceIDs[resource.ID] {
			add(SeverityError, field+".id", fmt.Sprintf("duplicate resource id %q within config", resource.ID))
		}
		resourceIDs[resource.ID] = true

		if resource.Type == "" {
			add(SeverityError, field+".type", "missing")
		}
		if resource.Connector == "" {
			add(SeverityError, field+".connector", "missing")
		}
		if resource.Project != "" && resource.Project != pc.Project.ID {
			add(SeverityWarning, field+".project",
				fmt.Sprintf("references project %q, but this config declares project %q", resource.Project, pc.Project.ID))
		}
		if port, ok := resourcePort(resource); ok && port == 0 {
			add(SeverityError, field+".config.port",
				"port is 0; omit the port field entirely for resources that do not listen on a TCP port")
		}
		if resource.Connector == "local" && resource.Type == "process" {
			for _, warning := range localconn.ProcessConfigWarnings(resource.Config) {
				add(SeverityWarning, unknownFieldName, field+": "+warning)
			}
		}
		if _, ok := resource.Config["build"]; ok {
			add(SeverityWarning, field+".config.build",
				"`build` is deprecated and auto-translated to a legacy_command build_strategy; migrate to an explicit build_strategy (go_standard, make_standard, or legacy_command)")
		}
		for _, dep := range resource.DependsOn {
			if !bundleHasResource(pc.Resources, dep) {
				add(SeverityWarning, field+".depends_on",
					fmt.Sprintf("dependency %q is not defined in this config (may be cross-config)", dep))
			}
		}
	}

	for _, conflict := range PortConflicts(pc.Resources) {
		add(SeverityWarning, duplicatePortField, conflict.String())
	}

	// --- pipelines ---
	for i, pipeline := range pc.Pipelines {
		field := fmt.Sprintf("pipelines[%d]", i)
		if pipeline.ID == "" {
			add(SeverityError, field+".id", "missing")
		}
		for j, stage := range pipeline.Stages {
			for k, action := range stage.Actions {
				if action.Resource == "" {
					continue
				}
				if !resourceIDs[action.Resource] {
					add(SeverityWarning,
						fmt.Sprintf("%s.stages[%d].actions[%d].resource", field, j, k),
						fmt.Sprintf("references resource %q not defined in this config", action.Resource))
				}
			}
		}
	}

	return result
}

// ValidateBundle checks a bundle manifest against the cerberus-bundle/v1
// kind contract. The manifest is intentionally minimal: a kind and a
// non-empty list of project-config paths. It does not check that the
// referenced files exist — that is register / resolve's job.
func ValidateBundle(bundle *Bundle) ValidationResult {
	var result ValidationResult
	sink := issueSink{result: &result}
	add := sink.add

	if bundle == nil {
		add(SeverityError, "bundle", "bundle is nil")
		return result
	}

	for _, f := range bundle.UnknownFields {
		add(SeverityWarning, unknownFieldName, f)
	}

	switch bundle.Kind {
	case BundleKind:
		// ok
	case "":
		add(SeverityError, "kind", fmt.Sprintf("missing; expected %q", BundleKind))
	default:
		add(SeverityError, "kind", fmt.Sprintf("unsupported kind %q; expected %q", bundle.Kind, BundleKind))
	}

	if len(bundle.Projects) == 0 {
		add(SeverityError, "projects", "manifest references no project configs")
	}
	for i, p := range bundle.Projects {
		if p == "" {
			add(SeverityError, fmt.Sprintf("projects[%d]", i), "empty path")
		}
	}
	return result
}

// resourcePort extracts a numeric `port` from a resource's connector
// config map. The map is open (connector-specific) and YAML decodes
// integers to varying Go types, so several numeric kinds are handled.
// The bool return distinguishes "absent" from "present and zero" — only
// the latter is a violation.
func resourcePort(resource config.ResourceDef) (int64, bool) {
	raw, ok := resource.Config["port"]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case uint64:
		return int64(v), true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}

// bundleHasResource reports whether id matches any resource in the
// slice. It scans the whole slice so forward references within a config
// are not mistaken for cross-config dependencies.
func bundleHasResource(resources []config.ResourceDef, id string) bool {
	for _, r := range resources {
		if r.ID == id {
			return true
		}
	}
	return false
}
