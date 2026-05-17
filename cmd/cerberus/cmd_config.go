package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/registry"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// migrateFallbackOwner owns resources whose project is missing or
// unknown during `config migrate`, so no resource is silently dropped.
const migrateFallbackOwner = "unassigned"

var configCmd = &cobra.Command{
	Use:     "config",
	Short:   "Inspect and migrate Cerberus configuration",
	GroupID: "runtime",
}

var configValidateCmd = &cobra.Command{
	Use:   "validate [path]",
	Short: "Validate registered configs, or a single config file",
	Long: `With no argument, validates every registered project config and reports
the resolved runtime config. With a path, validates just that file
(project config or bundle manifest) — useful in CI / pre-commit before
the file is registered.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConfigValidate,
}

var configMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Split the monolithic config.yaml into per-project .cerberus.yaml files",
	Long: `Splits the monolithic config.yaml into one project config per project,
writes them under <config-dir>/projects/, registers each, and backs up
the original to config.yaml.bak.

Use --dry-run first to preview the split without writing anything.`,
	Args: cobra.NoArgs,
	RunE: runConfigMigrate,
}

func init() {
	configCmd.AddCommand(configValidateCmd)
	configCmd.AddCommand(configMigrateCmd)
	configMigrateCmd.Flags().Bool("dry-run", false, "preview the migration without writing files")
}

// runConfigValidate backs both `cerberus config validate` and the
// top-level `cerberus validate`.
func runConfigValidate(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		return validateOneFile(args[0])
	}
	return validateRegistered()
}

// validateOneFile validates a single project config or bundle manifest.
func validateOneFile(path string) error {
	kind, err := registry.PeekKind(path)
	if err != nil {
		return err
	}
	var result registry.ValidationResult
	switch kind {
	case registry.ProjectConfigKind:
		pc, loadErr := registry.LoadProjectConfig(path)
		if loadErr != nil {
			return loadErr
		}
		result = registry.ValidateProjectConfig(pc)
	case registry.BundleKind:
		bundle, loadErr := registry.LoadBundle(path)
		if loadErr != nil {
			return loadErr
		}
		result = registry.ValidateBundle(bundle)
	default:
		return fmt.Errorf("%s declares unknown kind %q", path, kind)
	}

	printIssues(path, result)
	if result.HasErrors() {
		return fmt.Errorf("%s has %d error(s)", path, len(result.Errors()))
	}
	fmt.Printf("%s: OK\n", path)
	return nil
}

// validateRegistered health-checks every registered config and reports
// the assembled runtime config.
func validateRegistered() error {
	reg, err := registry.ForConfig(cfgPath)
	if err != nil {
		return err
	}
	reports, err := reg.Health()
	if err != nil {
		return err
	}
	unhealthy := 0
	for _, r := range reports {
		if !r.Healthy() {
			unhealthy++
			fmt.Fprintf(os.Stderr, "  %s: %s — %s\n", r.Owner, r.Status, r.Detail)
		}
	}

	indexPath, err := registry.IndexPathFor(cfgPath)
	if err != nil {
		return err
	}
	resolved, err := registry.Resolve(registry.ResolveOptions{IndexPath: indexPath, GlobalPath: cfgPath})
	if err != nil {
		return fmt.Errorf("resolve config: %w", err)
	}
	for _, w := range resolved.Warnings {
		fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
	}

	if unhealthy > 0 {
		return fmt.Errorf("%d registered config(s) unhealthy", unhealthy)
	}
	fmt.Printf("Config OK: %d registered, resolved to %d projects, %d resources\n",
		len(reports), len(resolved.Config.Projects), len(resolved.Config.Resources))
	return nil
}

func printIssues(path string, result registry.ValidationResult) {
	for _, issue := range result.Issues {
		fmt.Fprintf(os.Stderr, "  %s: %s — %s\n", issue.Severity, issue.Field, issue.Message)
	}
}

func runConfigMigrate(cmd *cobra.Command, args []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	cfg, err := config.LoadUnified(cfgPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", cfgPath, err)
	}

	configs, warnings := buildProjectConfigs(cfg)
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
	}
	if len(configs) == 0 {
		return fmt.Errorf("nothing to migrate: %s declares no projects or resources", cfgPath)
	}

	// Validate every generated config up front — migration is
	// all-or-nothing, so a single invalid split aborts before any write.
	invalid := 0
	for _, pc := range configs {
		result := registry.ValidateProjectConfig(pc)
		for _, issue := range result.Errors() {
			invalid++
			fmt.Fprintf(os.Stderr, "  error: %s: %s — %s\n", pc.Owner, issue.Field, issue.Message)
		}
	}
	if invalid > 0 {
		return fmt.Errorf("migration aborted: %d error(s) in the generated configs", invalid)
	}

	projectsDir := filepath.Join(filepath.Dir(cfgPath), "projects")
	backupPath := cfgPath + ".bak"

	total := 0
	for _, pc := range configs {
		total += len(pc.Resources)
	}
	fmt.Printf("migrate plan: %d resources -> %d project configs\n", total, len(configs))
	for _, pc := range configs {
		fmt.Printf("  %s (%d resources) -> %s\n",
			pc.Owner, len(pc.Resources), filepath.Join(projectsDir, pc.Owner+registry.FileSuffix))
	}
	if dryRun {
		fmt.Println("dry-run: no files written, nothing registered")
		return nil
	}

	if _, statErr := os.Stat(backupPath); statErr == nil {
		return fmt.Errorf("backup %s already exists; move it aside before migrating", backupPath)
	}
	if err := os.MkdirAll(projectsDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", projectsDir, err)
	}

	reg, err := registry.ForConfig(cfgPath)
	if err != nil {
		return err
	}

	// Write all project configs, then register them, then back up the
	// monolith last — config.yaml stays intact until everything else
	// has succeeded.
	written := make([]string, 0, len(configs))
	for _, pc := range configs {
		dest := filepath.Join(projectsDir, pc.Owner+registry.FileSuffix)
		data, marshalErr := yaml.Marshal(pc)
		if marshalErr != nil {
			return fmt.Errorf("marshal %s: %w", pc.Owner, marshalErr)
		}
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		written = append(written, dest)
	}
	for _, dest := range written {
		if _, err := reg.Register(dest); err != nil {
			return fmt.Errorf("register %s: %w", dest, err)
		}
	}
	if err := os.Rename(cfgPath, backupPath); err != nil {
		return fmt.Errorf("back up %s: %w", cfgPath, err)
	}

	fmt.Printf("migrated: %d project configs written to %s and registered\n", len(written), projectsDir)
	fmt.Printf("backed up: %s -> %s\n", cfgPath, backupPath)
	fmt.Println("the registry is now the source of truth; move each project config into its app's repo and re-register when ready")
	return nil
}

// buildProjectConfigs splits a monolithic ConfigV2 into one ProjectConfig
// per project. Resources are grouped by their Project field; resources
// with a missing or unknown project are assigned to a fallback owner so
// none are dropped. Pipelines follow the project of the first resource
// they act on.
func buildProjectConfigs(cfg *config.ConfigV2) ([]*registry.ProjectConfig, []string) {
	var warnings []string
	byOwner := map[string]*registry.ProjectConfig{}
	order := make([]string, 0, len(cfg.Projects)+1)

	add := func(id, name string) *registry.ProjectConfig {
		pc := &registry.ProjectConfig{
			Kind:      registry.ProjectConfigKind,
			Owner:     id,
			Namespace: registry.DefaultNamespace,
			Project:   config.ProjectDef{ID: id, Name: name},
		}
		byOwner[id] = pc
		order = append(order, id)
		return pc
	}

	for _, p := range cfg.Projects {
		pc := add(p.ID, p.Name)
		pc.Project = p
	}

	fallback := func() *registry.ProjectConfig {
		if pc, ok := byOwner[migrateFallbackOwner]; ok {
			return pc
		}
		return add(migrateFallbackOwner, "Unassigned (migrated)")
	}

	// config.yaml may carry accidental duplicate resource entries — the
	// legacy loader tolerated them and the resolver dedups by ID, but a
	// project config with a repeated id fails validation. Collapse them
	// here so migrate stays consistent with the resolver.
	resources := make([]config.ResourceDef, 0, len(cfg.Resources))
	seen := map[string]config.ResourceDef{}
	for _, r := range cfg.Resources {
		if prev, dup := seen[r.ID]; dup {
			if reflect.DeepEqual(prev, r) {
				warnings = append(warnings, fmt.Sprintf(
					"resource %q has duplicate identical entries in config.yaml; collapsed to one", r.ID))
			} else {
				warnings = append(warnings, fmt.Sprintf(
					"resource %q has duplicate entries with differing fields in config.yaml; kept the first", r.ID))
			}
			continue
		}
		seen[r.ID] = r
		resources = append(resources, r)
	}

	for _, r := range resources {
		if pc, ok := byOwner[r.Project]; ok && r.Project != "" {
			pc.Resources = append(pc.Resources, r)
			continue
		}
		warnings = append(warnings, fmt.Sprintf(
			"resource %q has no known project (project=%q); assigned to owner %q",
			r.ID, r.Project, migrateFallbackOwner))
		orphan := r
		orphan.Project = migrateFallbackOwner
		fb := fallback()
		fb.Resources = append(fb.Resources, orphan)
	}

	for _, pl := range cfg.Pipelines {
		owner := pipelineProjectOwner(pl, cfg)
		pc, ok := byOwner[owner]
		if !ok {
			if len(order) == 0 {
				warnings = append(warnings, fmt.Sprintf("pipeline %q dropped: no project to attach it to", pl.ID))
				continue
			}
			warnings = append(warnings, fmt.Sprintf(
				"pipeline %q has no resolvable project; assigned to owner %q", pl.ID, order[0]))
			pc = byOwner[order[0]]
		}
		pc.Pipelines = append(pc.Pipelines, pl)
	}

	out := make([]*registry.ProjectConfig, 0, len(order))
	for _, id := range order {
		out = append(out, byOwner[id])
	}
	return out, warnings
}

// pipelineProjectOwner returns the project of the first resource any of
// the pipeline's actions reference, or "" when none resolves.
func pipelineProjectOwner(pl config.PipelineDef, cfg *config.ConfigV2) string {
	for _, stage := range pl.Stages {
		for _, action := range stage.Actions {
			if action.Resource == "" {
				continue
			}
			for _, r := range cfg.Resources {
				if r.ID == action.Resource {
					return r.Project
				}
			}
		}
	}
	return ""
}
