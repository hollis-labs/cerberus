package main

import (
	"fmt"
	"os"
	"time"

	"github.com/chrispian/cerberus/internal/pausectl"
	"github.com/spf13/cobra"
)

var restartTag string

var restartCmd = &cobra.Command{
	Use:   "restart [service...]",
	Short: "Restart legacy services headlessly",
	Long:  "Stops then starts legacy v1 services defined under services:. Use --tag to filter by tag. For modern local process resources, use `cerberus resource apply`.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		targets := filterServices(services, args, restartTag)
		if len(targets) == 0 {
			fmt.Println("No matching services found.")
			return nil
		}

		// Pause auto-restart for targets so the daemon monitor doesn't
		// race us by restarting with the old binary mid-cycle.
		for _, svc := range targets {
			_ = pausectl.PauseService(svc.Def.ID)
		}
		defer func() {
			for _, svc := range targets {
				_ = pausectl.ResumeService(svc.Def.ID)
			}
		}()

		for _, svc := range targets {
			svc.Poll()
			fmt.Printf("%-20s restarting...\n", svc.Def.ID)
			svc.Stop() //nolint:errcheck
			time.Sleep(500 * time.Millisecond)
			if err := svc.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "%-20s start error: %v\n", svc.Def.ID, err)
			}
		}
		return nil
	},
}

func init() {
	restartCmd.Flags().StringVar(&restartTag, "tag", "", "filter services by tag")
}
