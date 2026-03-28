package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chrispian/cerberus/internal/pausectl"
	"github.com/spf13/cobra"
)

var rebuildTag string

var rebuildCmd = &cobra.Command{
	Use:   "rebuild [service...]",
	Short: "Build then restart services",
	Long:  "Builds specified services, then stops and restarts them. If build fails, the service is NOT restarted. Use --tag to filter by tag.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		targets := filterServices(services, args, rebuildTag)
		if len(targets) == 0 {
			fmt.Println("No matching services found.")
			return nil
		}

		// Pause auto-restart for targets so the daemon monitor doesn't
		// race us by restarting with the old binary mid-build.
		for _, svc := range targets {
			_ = pausectl.PauseService(svc.Def.ID)
		}
		defer func() {
			for _, svc := range targets {
				_ = pausectl.ResumeService(svc.Def.ID)
			}
		}()

		hasError := false
		for _, svc := range targets {
			if len(svc.Def.Build) == 0 {
				fmt.Printf("%-20s no build command, restarting only...\n", svc.Def.ID)
			} else {
				fmt.Printf("%-20s building...\n", svc.Def.ID)
				out, err := svc.BuildSync()
				if err != nil {
					fmt.Fprintf(os.Stderr, "%-20s build failed, skipping restart:\n%s\n", svc.Def.ID, strings.TrimSpace(out))
					hasError = true
					continue
				}
				fmt.Printf("%-20s build ok\n", svc.Def.ID)
			}

			svc.Poll()
			svc.Stop() //nolint:errcheck
			time.Sleep(500 * time.Millisecond)
			if err := svc.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "%-20s start error: %v\n", svc.Def.ID, err)
				hasError = true
			} else {
				fmt.Printf("%-20s restarted\n", svc.Def.ID)
			}
		}
		if hasError {
			return fmt.Errorf("one or more rebuild-restarts failed")
		}
		return nil
	},
}

func init() {
	rebuildCmd.Flags().StringVar(&rebuildTag, "tag", "", "filter services by tag")
}
