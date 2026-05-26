package main

import (
	"fmt"
	"os"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create default config",
	Long: `Creates the default configuration file if it does not exist.

The seed config is an empty v2 layout (` + "`version: 2`" + `, no projects, no
resources). After init, register one or more existing project configs or edit
the seed directly to add projects.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := config.DefaultPath()
		alreadyExisted := fileExists(path)

		if err := config.EnsureDefault(); err != nil {
			return fmt.Errorf("error creating config: %w", err)
		}

		if alreadyExisted {
			fmt.Printf("Config already exists at %s — left unchanged.\n", path)
		} else {
			fmt.Printf("Config written to %s\n", path)
		}

		fmt.Println()
		fmt.Println("Next steps:")
		fmt.Printf("  1. Edit %s to add projects + resources, OR\n", path)
		fmt.Println("     register an existing project config:")
		fmt.Println("       cerberus register <path-to-project.yaml>")
		fmt.Println("  2. Bootstrap the macOS launch agent (one-time, optional):")
		fmt.Println("       cerberus install")
		fmt.Println("  3. List what's now visible:")
		fmt.Println("       cerberus resource list")
		fmt.Println()
		fmt.Println("See 'cerberus --help' or docs/install.md for the full tour.")
		return nil
	},
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
