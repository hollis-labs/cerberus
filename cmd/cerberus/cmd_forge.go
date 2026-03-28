package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/app"
	forgeconn "github.com/chrispian/cerberus/internal/connector/forge"
	"github.com/spf13/cobra"
)

var forgeCmd = &cobra.Command{
	Use:   "forge",
	Short: "Laravel Forge operations (read-only)",
}

var forgeServersCmd = &cobra.Command{
	Use:   "servers",
	Short: "List all Forge servers",
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		conn, err := forgeconn.New(a.Secrets)
		if err != nil {
			return err
		}

		servers, err := conn.ListServers(context.Background())
		if err != nil {
			return err
		}

		if len(servers) == 0 {
			fmt.Println("No servers found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tIP\tREGION\tSIZE\tPHP\tPROVIDER\tREADY")
		fmt.Fprintln(w, "--\t----\t--\t------\t----\t---\t--------\t-----")
		for _, s := range servers {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%v\n",
				s.ID, s.Name, s.IP, s.Region, s.Size, s.PHP, s.Provider, s.IsReady)
		}
		return w.Flush()
	},
}

var forgeServerCmd = &cobra.Command{
	Use:   "server <server-id>",
	Short: "Show Forge server details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		serverID, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid server ID %q: %w", args[0], err)
		}

		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		conn, err := forgeconn.New(a.Secrets)
		if err != nil {
			return err
		}

		server, err := conn.GetServer(context.Background(), serverID)
		if err != nil {
			return err
		}

		data, _ := json.MarshalIndent(server, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

var forgeSitesCmd = &cobra.Command{
	Use:   "sites <server-id>",
	Short: "List sites on a Forge server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		serverID, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid server ID %q: %w", args[0], err)
		}

		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		conn, err := forgeconn.New(a.Secrets)
		if err != nil {
			return err
		}

		sites, err := conn.ListSites(context.Background(), serverID)
		if err != nil {
			return err
		}

		if len(sites) == 0 {
			fmt.Println("No sites found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tREPOSITORY\tBRANCH\tSTATUS\tDEPLOY STATUS")
		fmt.Fprintln(w, "--\t----\t----------\t------\t------\t-------------")
		for _, s := range sites {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
				s.ID, s.Name, s.Repository, s.Branch, s.Status, s.DeploymentStatus)
		}
		return w.Flush()
	},
}

func init() {
	forgeCmd.AddCommand(forgeServersCmd)
	forgeCmd.AddCommand(forgeServerCmd)
	forgeCmd.AddCommand(forgeSitesCmd)
}
