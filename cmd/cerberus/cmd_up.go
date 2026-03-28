package main

import (
	"fmt"
	"os"

	"github.com/chrispian/cerberus/internal/service"
	"github.com/spf13/cobra"
)

var upTag string

var upCmd = &cobra.Command{
	Use:   "up [service...]",
	Short: "Start services headlessly",
	Long:  "Starts specified services (or all if none given) in dependency order. Use --tag to filter by tag.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		targets := filterServices(services, args, upTag)
		if len(targets) == 0 {
			fmt.Println("No matching services found.")
			return nil
		}

		// Use Manager for dependency-ordered startup.
		mgr, err := service.NewServiceManager(services)
		if err != nil {
			// Fallback: start without ordering if DAG fails.
			fmt.Fprintf(os.Stderr, "Warning: dependency ordering unavailable: %v\n", err)
			for _, svc := range targets {
				svc.Poll()
				if svc.Status == service.StatusRunning {
					fmt.Printf("%-20s already running (pid %d)\n", svc.Def.ID, svc.PID)
					continue
				}
				if err := svc.Start(); err != nil {
					fmt.Fprintf(os.Stderr, "%-20s error: %v\n", svc.Def.ID, err)
				} else {
					fmt.Printf("%-20s starting...\n", svc.Def.ID)
				}
			}
			return nil
		}

		// If starting all services, use StartAll for full dependency ordering.
		if len(args) == 0 && upTag == "" {
			errs := mgr.StartAll()
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "  error: %v\n", e)
			}
			if len(errs) == 0 {
				fmt.Printf("All %d services starting in dependency order.\n", len(targets))
			}
			return nil
		}

		// Starting specific services: start each with auto-deps.
		for _, svc := range targets {
			svc.Poll()
			if svc.Status == service.StatusRunning || svc.Status == service.StatusHealthy {
				fmt.Printf("%-20s already running (pid %d)\n", svc.Def.ID, svc.PID)
				continue
			}
			errs := mgr.StartService(svc.Def.ID, true)
			if len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintf(os.Stderr, "%-20s error: %v\n", svc.Def.ID, e)
				}
			} else {
				fmt.Printf("%-20s starting...\n", svc.Def.ID)
			}
		}
		return nil
	},
}

func init() {
	upCmd.Flags().StringVar(&upTag, "tag", "", "filter services by tag")
}
