package registry

import (
	"fmt"
	"os"
	"sort"

	"github.com/hollis-labs/cerberus/internal/config"
)

// ResolveOptions selects the sources the resolver merges.
type ResolveOptions struct {
	// IndexPath is the registry index. Empty uses DefaultIndexPath.
	IndexPath string
	// GlobalPath is the optional monolithic config.yaml. Empty, or a
	// path that does not exist, means "no global source" — resolution
	// proceeds from the registry alone.
	GlobalPath string
}

// ResolvedConfig is the merged runtime config plus diagnostics.
type ResolvedConfig struct {
	// Config is the assembled, normalized ConfigV2.
	Config *config.ConfigV2
	// Warnings records non-fatal events: registered configs skipped
	// because they no longer load, registered-vs-registered id
	// collisions resolved by precedence, and warning-severity
	// validation issues on configs that did resolve.
	Warnings []string
	// Skipped lists registered entries dropped from the result because
	// their file is missing or invalid.
	Skipped []HealthReport
	// Warned lists registered entries that resolved but carry
	// warning-severity validation issues — most commonly a field this
	// binary does not know, written by a newer writer. These are in the
	// result; the entry is here so a caller can say so rather than
	// present a clean list over a config nobody has looked at.
	Warned []HealthReport
}

// Resolve assembles the effective ConfigV2 from the optional global
// config.yaml plus every registered project config.
//
// Precedence: a registered project config wins over the global file
// (the incremental-migration rule) — registering an app lets it take
// over its own ids while config.yaml shrinks. A broken registered
// config is skipped with a warning rather than failing the whole
// resolve, so one app's bad file cannot wedge the runtime — an
// isolation property the monolith never had. A broken global file is
// still fatal: it is operator-owned and may carry many resources.
func Resolve(opts ResolveOptions) (*ResolvedConfig, error) {
	indexPath := opts.IndexPath
	if indexPath == "" {
		p, err := DefaultIndexPath()
		if err != nil {
			return nil, err
		}
		indexPath = p
	}
	idx, err := LoadIndex(indexPath)
	if err != nil {
		return nil, err
	}

	return resolveIndex(opts, idx)
}

func resolveIndex(opts ResolveOptions, idx *Index) (*ResolvedConfig, error) {
	resolved := &ResolvedConfig{Config: &config.ConfigV2{Version: 2}}

	projects := map[string]config.ProjectDef{}
	resources := map[string]config.ResourceDef{}
	pipelines := map[string]config.PipelineDef{}
	// owner of each id: "" = global config.yaml, else the project
	// config's owner. Used to classify override events.
	projectOwner := map[string]string{}
	resourceOwner := map[string]string{}
	pipelineOwner := map[string]string{}

	// 1. global config.yaml — applied first so registered configs layer
	//    on top. Absent file is fine; a present-but-broken file is not.
	if opts.GlobalPath != "" {
		if _, statErr := os.Stat(opts.GlobalPath); statErr == nil {
			global, loadErr := config.LoadUnified(opts.GlobalPath)
			if loadErr != nil {
				return nil, fmt.Errorf("load global config %s: %w", opts.GlobalPath, loadErr)
			}
			resolved.Config.Build = global.Build
			for _, p := range global.Projects {
				projects[p.ID] = p
				projectOwner[p.ID] = ""
			}
			for _, r := range global.Resources {
				resources[r.ID] = r
				resourceOwner[r.ID] = ""
			}
			for _, pl := range global.Pipelines {
				pipelines[pl.ID] = pl
				pipelineOwner[pl.ID] = ""
			}
		}
	}

	// 2. registered project configs, owner-sorted for deterministic
	//    collision resolution.
	entries := append([]IndexEntry(nil), idx.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Owner < entries[j].Owner })

	for _, entry := range entries {
		if err := ValidateConfigLocation(entry.Path, opts.IndexPath); err != nil {
			resolved.skip(entry, HealthInvalid, err.Error())
			continue
		}
		pc, loadErr := LoadProjectConfig(entry.Path)
		if loadErr != nil {
			resolved.skip(entry, HealthMissing, loadErr.Error())
			continue
		}
		vr := ValidateProjectConfig(pc)
		if vr.HasErrors() {
			resolved.skip(entry, HealthInvalid, vr.Errors()[0].String())
			continue
		}
		if issues := vr.Warnings(); len(issues) > 0 {
			resolved.warnConfig(entry, issues)
		}

		if prev := projectOwner[pc.Project.ID]; prev != "" {
			resolved.warnOverride("project", pc.Project.ID, pc.Owner, prev)
		}
		projects[pc.Project.ID] = pc.Project
		projectOwner[pc.Project.ID] = pc.Owner

		for _, r := range pc.Resources {
			if prev := resourceOwner[r.ID]; prev != "" {
				resolved.warnOverride("resource", r.ID, pc.Owner, prev)
			}
			resources[r.ID] = r
			resourceOwner[r.ID] = pc.Owner
		}
		for _, pl := range pc.Pipelines {
			if prev := pipelineOwner[pl.ID]; prev != "" {
				resolved.warnOverride("pipeline", pl.ID, pc.Owner, prev)
			}
			pipelines[pl.ID] = pl
			pipelineOwner[pl.ID] = pc.Owner
		}
	}

	resolved.Config.Projects = flattenProjects(projects)
	resolved.Config.Resources = flattenResources(resources)
	// A label outside the vocabulary reads as unknown. The global config
	// is not validated like a project config, so its problems are reported
	// here; a project config's were reported by ValidateProjectConfig.
	for i := range resolved.Config.Resources {
		r := &resolved.Config.Resources[i]
		labels := r.TargetLabels()
		if resourceOwner[r.ID] == "" {
			for _, problem := range labels.Validate() {
				resolved.Warnings = append(resolved.Warnings, fmt.Sprintf("resource %q: %s; read as unknown", r.ID, problem))
			}
		}
		clean := labels.Sanitized()
		r.Env, r.Owner, r.Admin = clean.Env, clean.Owner, clean.Admin
	}
	resolved.Config.Pipelines = flattenPipelines(pipelines)
	config.NormalizeV2(resolved.Config)
	for _, conflict := range PortConflicts(resolved.Config.Resources) {
		resolved.Warnings = append(resolved.Warnings, conflict.String())
	}
	return resolved, nil
}

// ResolveConfig is the registry-aware replacement for config.LoadUnified
// on the runtime path. It assembles the effective config from the
// global config.yaml at globalPath plus every registered project
// config. The registry index is taken as a sibling of globalPath
// (~/.cerberus/registry.yaml next to ~/.cerberus/config.yaml), which
// keeps resolution hermetic: a test or `--config` override that
// relocates config.yaml relocates the registry with it.
func ResolveConfig(globalPath string) (*config.ConfigV2, error) {
	resolved, err := ResolveConfigDetailed(globalPath)
	if err != nil {
		return nil, err
	}
	return resolved.Config, nil
}

// ResolveConfigDetailed is ResolveConfig with the diagnostics kept.
//
// ResolveConfig throws Warnings and Skipped away, which is what made a
// dropped config invisible: the operator saw a short list, not a short
// list plus "2 configs were skipped". Callers that render a list should
// use this and report what resolution dropped.
func ResolveConfigDetailed(globalPath string) (*ResolvedConfig, error) {
	indexPath, err := IndexPathFor(globalPath)
	if err != nil {
		return nil, err
	}
	return Resolve(ResolveOptions{IndexPath: indexPath, GlobalPath: globalPath})
}

func (r *ResolvedConfig) skip(entry IndexEntry, status, detail string) {
	r.Skipped = append(r.Skipped, HealthReport{
		Owner: entry.Owner, Path: entry.Path, Status: status, Detail: detail,
	})
	r.Warnings = append(r.Warnings, fmt.Sprintf("skipped owner %q (%s): %s", entry.Owner, status, detail))
}

func (r *ResolvedConfig) warnConfig(entry IndexEntry, issues []ValidationIssue) {
	r.Warned = append(r.Warned, HealthReport{
		Owner: entry.Owner, Path: entry.Path, Status: HealthOK,
		Detail: fmt.Sprintf("%d warning(s): %s", len(issues), issues[0].Message),
	})
	for _, issue := range issues {
		r.Warnings = append(r.Warnings,
			fmt.Sprintf("owner %q: %s — %s", entry.Owner, issue.Field, issue.Message))
	}
}

func (r *ResolvedConfig) warnOverride(kind, id, winner, loser string) {
	r.Warnings = append(r.Warnings,
		fmt.Sprintf("%s %q: owner %q overrides owner %q", kind, id, winner, loser))
}

func flattenProjects(m map[string]config.ProjectDef) []config.ProjectDef {
	out := make([]config.ProjectDef, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func flattenResources(m map[string]config.ResourceDef) []config.ResourceDef {
	out := make([]config.ResourceDef, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func flattenPipelines(m map[string]config.PipelineDef) []config.PipelineDef {
	out := make([]config.PipelineDef, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
