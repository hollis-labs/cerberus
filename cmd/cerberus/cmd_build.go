package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var buildTag string

var buildCmd = &cobra.Command{
	Use:   "build [service...]",
	Short: "Run build commands",
	Long:  "Runs build commands for specified services (or all if none given). Use --tag to filter by tag.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		targets := filterServices(services, args, buildTag)
		if len(targets) == 0 {
			fmt.Println("No matching services found.")
			return nil
		}

		hasError := false
		for _, svc := range targets {
			if len(svc.Def.Build) == 0 {
				fmt.Printf("%-20s no build command configured, skipping\n", svc.Def.ID)
				continue
			}
			fmt.Printf("%-20s building...\n", svc.Def.ID)
			out, err := svc.BuildSync()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%-20s build failed:\n%s\n", svc.Def.ID, strings.TrimSpace(out))
				hasError = true
			} else {
				fmt.Printf("%-20s build ok\n", svc.Def.ID)
			}
		}
		if hasError {
			return fmt.Errorf("one or more builds failed")
		}
		return nil
	},
}

func init() {
	buildCmd.Flags().StringVar(&buildTag, "tag", "", "filter services by tag")
}
