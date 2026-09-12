package main

import (
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/redact"

	"github.com/chrispian/cerberus/internal/cerbapi"
	forgeconn "github.com/chrispian/cerberus/internal/connector/forge"
	"github.com/spf13/cobra"
)

var forgeCmd = &cobra.Command{
	Use:   "forge",
	Short: "Laravel Forge operations",
}

var forgeServersCmd = &cobra.Command{
	Use:   "servers",
	Short: "List all Forge servers",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{Connector: "forge", Operation: "list_servers"})
		if err != nil {
			return err
		}
		servers, ok := result.Data.([]forgeconn.Server)
		if !ok {
			return fmt.Errorf("forge servers: unexpected result type %T", result.Data)
		}
		if len(servers) == 0 {
			fmt.Println("No servers found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tIP\tREGION\tSIZE\tPHP\tPROVIDER\tREADY")
		fmt.Fprintln(w, "--\t----\t--\t------\t----\t---\t--------\t-----")
		for _, s := range servers {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%v\n", s.ID, s.Name, s.IP, s.Region, s.Size, s.PHP, s.Provider, s.IsReady)
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
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "forge",
			Operation: "get_server",
			Config:    map[string]any{"server_id": serverID},
		})
		if err != nil {
			return err
		}
		server, ok := result.Data.(*forgeconn.Server)
		if !ok {
			return fmt.Errorf("forge server: unexpected result type %T", result.Data)
		}
		return writeJSON(cmd.OutOrStdout(), server)
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
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "forge",
			Operation: "list_sites",
			Config:    map[string]any{"server_id": serverID},
		})
		if err != nil {
			return err
		}
		sites, ok := result.Data.([]forgeconn.Site)
		if !ok {
			return fmt.Errorf("forge sites: unexpected result type %T", result.Data)
		}
		if len(sites) == 0 {
			fmt.Println("No sites found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tREPOSITORY\tBRANCH\tSTATUS\tDEPLOY STATUS")
		fmt.Fprintln(w, "--\t----\t----------\t------\t------\t-------------")
		for _, s := range sites {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Name, s.Repository, s.Branch, s.Status, s.DeploymentStatus)
		}
		return w.Flush()
	},
}

var forgeDeployCmd = &cobra.Command{
	Use:   "deploy <server-id> <site-id>",
	Short: "Trigger a Forge site deployment",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		serverID, siteID, err := parseServerSiteIDs(args[0], args[1])
		if err != nil {
			return err
		}
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()
		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector:    "forge",
			Operation:    "deploy_site",
			Config:       map[string]any{"server_id": serverID, "site_id": siteID},
			DryRun:       forgeDryRun,
			Acknowledged: forgeAcknowledge,
		})
		if err != nil {
			return err
		}
		if forgeDryRun {
			return writeJSON(cmd.OutOrStdout(), result.Data)
		}
		return nil
	},
}

var forgeScriptAutoSource bool
var forgeDryRun bool
var forgeAcknowledge bool

var forgeScriptGetCmd = &cobra.Command{
	Use:   "script <server-id> <site-id>",
	Short: "Read a site's deployment script",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		serverID, siteID, err := parseServerSiteIDs(args[0], args[1])
		if err != nil {
			return err
		}
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()
		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector:    "forge",
			Operation:    "get_deployment_script",
			Config:       map[string]any{"server_id": serverID, "site_id": siteID},
			Acknowledged: forgeAcknowledge,
		})
		if err != nil {
			return err
		}
		script, ok := result.Data.(string)
		if !ok {
			return fmt.Errorf("forge script: unexpected result type %T", result.Data)
		}
		fmt.Fprintln(cmd.OutOrStdout(), script)
		return nil
	},
}

var forgeScriptSetCmd = &cobra.Command{
	Use:   "set-script <server-id> <site-id> <content>",
	Short: "Update a site's deployment script",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		serverID, siteID, err := parseServerSiteIDs(args[0], args[1])
		if err != nil {
			return err
		}
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()
		_, err = svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "forge",
			Operation: "update_deployment_script",
			Config: map[string]any{
				"server_id":   serverID,
				"site_id":     siteID,
				"content":     args[2],
				"auto_source": forgeScriptAutoSource,
			},
			Acknowledged: forgeAcknowledge,
		})
		return err
	},
}

var forgeExecCmd = &cobra.Command{
	Use:   "exec <server-id> <site-id> <command>",
	Short: "Execute a command on a Forge site",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		serverID, siteID, err := parseServerSiteIDs(args[0], args[1])
		if err != nil {
			return err
		}
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()
		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "forge",
			Operation: "exec_site_command",
			Config: map[string]any{
				"server_id": serverID,
				"site_id":   siteID,
				"command":   args[2],
			},
			DryRun:       forgeDryRun,
			Acknowledged: forgeAcknowledge,
		})
		if err != nil {
			return err
		}
		if forgeDryRun {
			return writeJSON(cmd.OutOrStdout(), result.Data)
		}
		command, ok := result.Data.(*forgeconn.SiteCommand)
		if !ok {
			return fmt.Errorf("forge exec: unexpected result type %T", result.Data)
		}
		data, _ := redact.MarshalIndent(command, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	},
}

func parseServerSiteIDs(server, site string) (int, int, error) {
	serverID, err := strconv.Atoi(server)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid server ID %q: %w", server, err)
	}
	siteID, err := strconv.Atoi(site)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid site ID %q: %w", site, err)
	}
	return serverID, siteID, nil
}

func init() {
	forgeScriptSetCmd.Flags().BoolVar(&forgeScriptAutoSource, "auto-source", false, "automatically source environment variables in the deployment script")
	forgeDeployCmd.Flags().BoolVar(&forgeDryRun, "dry-run", false, "preview the deployment without sending it to Forge")
	forgeDeployCmd.Flags().BoolVar(&forgeAcknowledge, "ack", false, "acknowledge destructive deployment action")
	forgeScriptSetCmd.Flags().BoolVar(&forgeAcknowledge, "ack", false, "acknowledge destructive deployment action")
	forgeExecCmd.Flags().BoolVar(&forgeDryRun, "dry-run", false, "preview the remote command without executing it on Forge")
	forgeExecCmd.Flags().BoolVar(&forgeAcknowledge, "ack", false, "acknowledge destructive deployment action")
	forgeCmd.AddCommand(forgeServersCmd)
	forgeCmd.AddCommand(forgeServerCmd)
	forgeCmd.AddCommand(forgeSitesCmd)
	forgeCmd.AddCommand(forgeDeployCmd)
	forgeCmd.AddCommand(forgeScriptGetCmd)
	forgeCmd.AddCommand(forgeScriptSetCmd)
	forgeCmd.AddCommand(forgeExecCmd)
}
