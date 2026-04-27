package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/service"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check legacy service health",
	Long:  "Checks ports, binaries, and working directories for legacy v1 services defined under services:. This does not yet validate v2 os_service install state.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		services := service.NewFromConfig(cfg)
		results := service.RunDoctor(services)

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "SERVICE\tCHECK\tSTATUS\tMESSAGE\n")
		for _, r := range results {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ServiceID, r.Check, r.Status, r.Message)
		}
		return w.Flush()
	},
}
