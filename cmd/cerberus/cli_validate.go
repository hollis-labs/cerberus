package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/registry"
)

// `cerberus validate [path]` and `cerberus config validate [path]` (old cmd_validate.go and
// cmd_config.go), with the old arguments and output. Like `registry health` they read the
// registry in the adopted root directly: validating a descriptor is a read of the operator's own
// files, and works with the server down. The config they resolve is the registry's; the old
// global config.yaml is replaced by the settings (conversion-notes, Paths).

// validateRunE validates every registered config, or with a path that one file.
func validateRunE(reg func() (*registry.Registry, error)) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		r, err := reg()
		if err != nil {
			return err
		}
		if len(args) == 1 {
			return validateOneFile(cmd.OutOrStdout(), cmd.ErrOrStderr(), r.IndexPath(), args[0])
		}
		return validateRegistered(cmd.OutOrStdout(), cmd.ErrOrStderr(), r)
	}
}

// validateCommands are `validate` and the `validate` that joins Substrate's `config`.
func validateCommands(reg func() (*registry.Registry, error)) (top, underConfig *cobra.Command) {
	top = &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate configuration",
		Long:  "Validates registered project configs, or a single config file when a path is given. Alias for `cerberus config validate`.",
		Args:  cobra.MaximumNArgs(1),
		RunE:  validateRunE(reg),
	}
	underConfig = &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate registered configs, or a single config file",
		Long: `With no argument, validates every registered project config and reports
the resolved runtime config. With a path, validates just that file
(project config or bundle manifest) — useful in CI / pre-commit before
the file is registered.`,
		Args: cobra.MaximumNArgs(1),
		RunE: validateRunE(reg),
	}
	return top, underConfig
}

// validateOneFile validates a single project config or bundle manifest (old's).
func validateOneFile(out, errOut io.Writer, indexPath, path string) error {
	if err := registry.ValidateConfigLocation(path, indexPath); err != nil {
		return err
	}
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
		for _, projectPath := range bundle.Projects {
			if lerr := registry.ValidateConfigLocation(projectPath, indexPath); lerr != nil {
				return lerr
			}
		}
	default:
		return fmt.Errorf("%s declares unknown kind %q; valid kinds are %q (single project) or %q (bundle of project paths)",
			path, kind, registry.ProjectConfigKind, registry.BundleKind)
	}
	for _, issue := range result.Issues {
		_, _ = fmt.Fprintf(errOut, "  %s: %s — %s\n", issue.Severity, issue.Field, issue.Message)
	}
	if conflicts := result.PortConflictIssues(); len(conflicts) > 0 {
		return fmt.Errorf("%s: %s", path, conflicts[0].Message)
	}
	if result.HasErrors() {
		return fmt.Errorf("%s has %d error(s)", path, len(result.Errors()))
	}
	// Author-time strictness: the runtime readers tolerate an unknown field so it can never drop
	// a project; validate is where a typo should still stop you. The message is old's, spelling
	// and all.
	if unknown := result.UnknownFieldIssues(); len(unknown) > 0 {
		return fmt.Errorf("%s has %d unrecognised field(s); fix the typo, or upgrade cerberus if the field is newer than this binary", //nolint:misspell // old's message
			path, len(unknown))
	}
	_, err = fmt.Fprintf(out, "%s: OK\n", path)
	return err
}

// validateRegistered health-checks every registered config and reports the resolved config.
func validateRegistered(out, errOut io.Writer, r *registry.Registry) error {
	reports, err := r.Health()
	if err != nil {
		return err
	}
	unhealthy := 0
	for _, rep := range reports {
		if !rep.Healthy() {
			unhealthy++
			_, _ = fmt.Fprintf(errOut, "  %s: %s — %s\n", rep.Owner, rep.Status, rep.Detail)
		}
	}
	resolved, err := r.Resolve()
	if err != nil {
		return fmt.Errorf("resolve config: %w", err)
	}
	for _, w := range resolved.Warnings {
		_, _ = fmt.Fprintf(errOut, "  warning: %s\n", w)
	}
	if unhealthy > 0 {
		return fmt.Errorf("%d registered config(s) unhealthy", unhealthy)
	}
	_, err = fmt.Fprintf(out, "Config OK: %d registered, resolved to %d projects, %d resources\n",
		len(reports), len(resolved.Config.Projects), len(resolved.Config.Resources))
	return err
}
