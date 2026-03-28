package main

import (
	"fmt"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create default config",
	Long:  "Creates the default configuration file if it does not exist.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.EnsureDefault(); err != nil {
			return fmt.Errorf("error creating config: %w", err)
		}
		fmt.Printf("Config written to %s\n", config.DefaultPath())
		return nil
	},
}
