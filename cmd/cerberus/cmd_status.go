package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show legacy service status table",
	Long:  "Prints a formatted table of legacy v1 services defined under services:. For modern local process resources, use `cerberus resource list` or `cerberus resource status`.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tPID\tPORT")
		fmt.Fprintln(w, "--\t----\t------\t---\t----")

		for _, svc := range services {
			svc.Poll()
			pid := "-"
			if svc.PID > 0 {
				pid = fmt.Sprintf("%d", svc.PID)
			}
			port := "-"
			if svc.Def.Port > 0 {
				port = fmt.Sprintf("%d", svc.Def.Port)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				svc.Def.ID, svc.Def.Name, svc.Status, pid, port)
		}
		w.Flush() //nolint:errcheck
		return nil
	},
}
