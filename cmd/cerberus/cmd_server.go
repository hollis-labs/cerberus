package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/app"
	doconn "github.com/chrispian/cerberus/internal/connector/digitalocean"
	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Cloud server operations",
}

var serverListCmd = &cobra.Command{
	Use:   "list",
	Short: "List cloud servers",
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		do, err := doconn.New(a.Secrets)
		if err != nil {
			return err
		}

		droplets, err := do.ListDroplets(context.Background())
		if err != nil {
			return err
		}

		if len(droplets) == 0 {
			fmt.Println("No droplets found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tREGION\tSIZE\tIPv4")
		fmt.Fprintln(w, "--\t----\t------\t------\t----\t----")
		for _, d := range droplets {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
				d.ID, d.Name, d.Status, d.Region, d.Size, d.IPv4)
		}
		return w.Flush()
	},
}

var serverShowCmd = &cobra.Command{
	Use:   "show <droplet-id>",
	Short: "Show server details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		do, err := doconn.New(a.Secrets)
		if err != nil {
			return err
		}

		// Parse droplet ID
		var id int
		if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil { //nolint:govet
			return fmt.Errorf("invalid droplet ID %q: expected integer", args[0])
		}

		droplet, err := do.GetDroplet(context.Background(), id)
		if err != nil {
			return err
		}

		data, _ := json.MarshalIndent(droplet, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

func init() {
	serverCmd.AddCommand(serverListCmd)
	serverCmd.AddCommand(serverShowCmd)
}
