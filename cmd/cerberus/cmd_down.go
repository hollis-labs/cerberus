package main

import (
	"fmt"
	"os"

	"github.com/chrispian/cerberus/internal/service"
	"github.com/spf13/cobra"
)

var downTag string

var downCmd = &cobra.Command{
	Use:   "down [service...]",
	Short: "Stop legacy services headlessly",
	Long:  "Stops legacy v1 services defined under services: in reverse dependency order. Use --tag to filter by tag. For modern local process resources, use `cerberus resource remove` or `cerberus resource status`.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		targets := filterServices(services, args, downTag)
		if len(targets) == 0 {
			fmt.Println("No matching services found.")
			return nil
		}

		// Use Manager for reverse-dependency ordered shutdown.
		mgr, err := service.NewServiceManager(services)
		if err != nil {
			// Fallback: stop without ordering if DAG fails.
			fmt.Fprintf(os.Stderr, "Warning: dependency ordering unavailable: %v\n", err)
			for _, svc := range targets {
				svc.Poll()
				if svc.Status == service.StatusStopped {
					fmt.Printf("%-20s already stopped\n", svc.Def.ID)
					continue
				}
				if err := svc.Stop(); err != nil {
					fmt.Fprintf(os.Stderr, "%-20s error: %v\n", svc.Def.ID, err)
				} else {
					fmt.Printf("%-20s stopping...\n", svc.Def.ID)
				}
			}
			return nil
		}

		// Explicit bulk stop: user issued `cerberus down` (all services).
		explicitBulk := len(args) == 0 && downTag == ""

		if explicitBulk {
			fmt.Printf("Stopping all %d services in reverse dependency order...\n", len(targets))
			errs := mgr.StopAll()
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "  error: %v\n", e)
			}
			return nil
		}

		// Stopping a subset: use StopSubset with bulk guard.
		errs := mgr.StopSubset(targets, false)
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  error: %v\n", e)
		}
		if len(errs) == 0 {
			for _, svc := range targets {
				fmt.Printf("%-20s stopping...\n", svc.Def.ID)
			}
		}
		return nil
	},
}

func init() {
	downCmd.Flags().StringVar(&downTag, "tag", "", "filter services by tag")
}
