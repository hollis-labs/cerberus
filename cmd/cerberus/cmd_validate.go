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
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("config error: %w", err)
		}

		// Check for common issues
		errors := 0
		ids := make(map[string]bool)
		ports := make(map[int]string)

		for _, svc := range cfg.Services {
			if svc.ID == "" {
				fmt.Fprintf(os.Stderr, "  error: service missing ID (name: %s)\n", svc.Name)
				errors++
			}
			if ids[svc.ID] {
				fmt.Fprintf(os.Stderr, "  error: duplicate service ID %q\n", svc.ID)
				errors++
			}
			ids[svc.ID] = true

			if svc.Port > 0 {
				if other, ok := ports[svc.Port]; ok {
					fmt.Fprintf(os.Stderr, "  error: port %d used by both %q and %q\n", svc.Port, other, svc.ID)
					errors++
				}
				ports[svc.Port] = svc.ID
			}

			if len(svc.Command) == 0 {
				fmt.Fprintf(os.Stderr, "  warning: %s has no command\n", svc.ID)
			}
			if svc.Dir == "" {
				fmt.Fprintf(os.Stderr, "  warning: %s has no dir\n", svc.ID)
			}
		}

		if errors > 0 {
			return fmt.Errorf("config has %d error(s)", errors)
		}

		fmt.Printf("Config OK: %d services defined\n", len(cfg.Services))
		return nil
	},
}
