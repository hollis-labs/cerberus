package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chrispian/cerberus/internal/configops"
	"github.com/chrispian/cerberus/internal/registry"
	"github.com/spf13/cobra"
)

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
		return fmt.Errorf("%s declares unknown kind %q; valid kinds are %q (single project) or %q (bundle of project paths)",
			path, kind, registry.ProjectConfigKind, registry.BundleKind)
	}

	printIssues(path, result)
	if conflicts := result.PortConflictIssues(); len(conflicts) > 0 {
		return fmt.Errorf("%s: %s", path, conflicts[0].Message)
	}
	if result.HasErrors() {
		return fmt.Errorf("%s has %d error(s)", path, len(result.Errors()))
	}
	// Author-time strictness. The runtime readers tolerate an
	// unrecognised field so it can never drop a project; validate is
	// where a typo should still stop you.
	if unknown := result.UnknownFieldIssues(); len(unknown) > 0 {
		return fmt.Errorf("%s has %d unrecognised field(s); fix the typo, or upgrade cerberus if the field is newer than this binary",
			path, len(unknown))
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

	preview, err := configops.PreviewMigration(cfgPath)
	if err != nil {
		return err
	}
	for _, w := range preview.Warnings {
		fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
	}
	for _, issue := range preview.ValidationErrors {
		fmt.Fprintf(os.Stderr, "  error: %s: %s — %s\n", issue.Owner, issue.Field, issue.Message)
	}
	if len(preview.ValidationErrors) > 0 {
		return fmt.Errorf("migration aborted: %d error(s) in the generated configs", len(preview.ValidationErrors))
	}
	fmt.Printf("migrate plan: %d resources -> %d project configs\n", preview.TotalResources, len(preview.ProjectConfigs))
	for _, pc := range preview.ProjectConfigs {
		fmt.Printf("  %s (%d resources) -> %s\n",
			pc.Owner, len(pc.Resources), filepath.Join(preview.ProjectsDir, pc.Owner+registry.FileSuffix))
	}
	if dryRun {
		fmt.Println("dry-run: no files written, nothing registered")
		return nil
	}

	result, err := configops.MigrateConfig(cfgPath)
	if err != nil {
		return err
	}
	fmt.Printf("migrated: %d project configs written to %s and registered\n", len(result.WrittenPaths), result.ProjectsDir)
	fmt.Printf("backed up: %s -> %s\n", cfgPath, result.BackupPath)
	fmt.Println("the registry is now the source of truth; move each project config into its app's repo and re-register when ready")
	return nil
}
