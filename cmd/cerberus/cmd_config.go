package main

import (
	"fmt"
	"os"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Configuration management",
}

var configMigrateWrite bool

var configMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate config from v1 to v2 format",
	Long:  "Reads the current config and outputs the v2 format. Use --write to overwrite the config file in place.",
	RunE: func(cmd *cobra.Command, args []string) error {
		v2, err := config.LoadUnified(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		data, err := yaml.Marshal(v2)
		if err != nil {
			return fmt.Errorf("marshal v2 config: %w", err)
		}

		if configMigrateWrite {
			if err := os.WriteFile(cfgPath, data, 0644); err != nil { //nolint:gosec
				return fmt.Errorf("write config: %w", err)
			}
			fmt.Printf("Config migrated to v2 and written to %s\n", cfgPath)
			return nil
		}

		fmt.Print(string(data))
		return nil
	},
}

func init() {
	configMigrateCmd.Flags().BoolVar(&configMigrateWrite, "write", false, "overwrite config file in place")
	configCmd.AddCommand(configMigrateCmd)
}
