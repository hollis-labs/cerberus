package main

import (
	"fmt"

	"github.com/chrispian/cerberus/internal/pausectl"
	"github.com/spf13/cobra"
)

var pauseCmd = &cobra.Command{
	Use:   "pause [service-id]",
	Short: "Pause auto-restart",
	Long:  "Pauses auto-restart for all services, or a specific service if an ID is given. While paused, services that crash will not be automatically restarted.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			serviceID := args[0]
			if err := pausectl.PauseService(serviceID); err != nil {
				return fmt.Errorf("pausing service %s: %w", serviceID, err)
			}
			fmt.Printf("Auto-restart paused for %s. Run 'cerberus resume %s' to re-enable.\n", serviceID, serviceID)
			return nil
		}
		if err := pausectl.PauseAll(); err != nil {
			return fmt.Errorf("pausing auto-restart: %w", err)
		}
		fmt.Println("Auto-restart paused. Run 'cerberus resume' to re-enable.")
		return nil
	},
}

var resumeCmd = &cobra.Command{
	Use:   "resume [service-id]",
	Short: "Resume auto-restart",
	Long:  "Resumes auto-restart for all services, or a specific service if an ID is given.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			serviceID := args[0]
			if err := pausectl.ResumeService(serviceID); err != nil {
				return fmt.Errorf("resuming service %s: %w", serviceID, err)
			}
			fmt.Printf("Auto-restart resumed for %s.\n", serviceID)
			return nil
		}
		if err := pausectl.ResumeAll(); err != nil {
			return fmt.Errorf("resuming auto-restart: %w", err)
		}
		fmt.Println("Auto-restart resumed.")
		return nil
	},
}
