package main

import "github.com/spf13/cobra"

// validateCmd is a top-level alias for `cerberus config validate`. With
// no argument it validates every registered project config; with a path
// it validates a single config file (project config or bundle manifest).
var validateCmd = &cobra.Command{
	Use:   "validate [path]",
	Short: "Validate configuration",
	Long:  "Validates registered project configs, or a single config file when a path is given. Alias for `cerberus config validate`.",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runConfigValidate,
}
