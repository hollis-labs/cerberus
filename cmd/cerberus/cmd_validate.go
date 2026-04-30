package main

import (
	"fmt"
	"os"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate configuration",
	Long:  "Loads and validates the config file, reporting any errors.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadUnified(cfgPath)
		if err != nil {
			return fmt.Errorf("config error: %w", err)
		}

		// Check for common issues
		errors := 0
		ids := make(map[string]bool)
		projectIDs := make(map[string]bool)

		for _, project := range cfg.Projects {
			if project.ID == "" {
				fmt.Fprintf(os.Stderr, "  error: project missing ID (name: %s)\n", project.Name)
				errors++
			}
			if projectIDs[project.ID] {
				fmt.Fprintf(os.Stderr, "  error: duplicate project ID %q\n", project.ID)
				errors++
			}
			projectIDs[project.ID] = true
		}

		for _, res := range cfg.Resources {
			if res.ID == "" {
				fmt.Fprintf(os.Stderr, "  error: resource missing ID (name: %s)\n", res.Name)
				errors++
			}
			if ids[res.ID] {
				fmt.Fprintf(os.Stderr, "  error: duplicate resource ID %q\n", res.ID)
				errors++
			}
			ids[res.ID] = true
			if res.Project != "" && !projectIDs[res.Project] {
				fmt.Fprintf(os.Stderr, "  warning: resource %s references unknown project %q\n", res.ID, res.Project)
			}
		}

		if errors > 0 {
			return fmt.Errorf("config has %d error(s)", errors)
		}

		fmt.Printf("Config OK: %d projects, %d resources defined\n", len(cfg.Projects), len(cfg.Resources))
		return nil
	},
}
