package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/redact"

	"github.com/chrispian/cerberus/internal/cerbapi"
	doconn "github.com/chrispian/cerberus/internal/connector/digitalocean"
	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "DigitalOcean droplet operations",
}

var serverListCmd = &cobra.Command{
	Use:   "list",
	Short: "List DigitalOcean droplets",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "digitalocean",
			Operation: "list_droplets",
		})
		if err != nil {
			return err
		}
		droplets, ok := result.Data.([]doconn.DropletStatus)
		if !ok {
			return fmt.Errorf("server list: unexpected result type %T", result.Data)
		}
		if len(droplets) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No droplets found.")
			return nil
		}

		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tREGION\tSIZE\tIPv4")
		fmt.Fprintln(w, "--\t----\t------\t------\t----\t----")
		for _, d := range droplets {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, d.Status, d.Region, d.Size, d.IPv4)
		}
		return w.Flush()
	},
}

var serverShowCmd = &cobra.Command{
	Use:   "show <droplet-id>",
	Short: "Show droplet details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()

		dropletID, err := parseIntArg(args[0], "droplet id")
		if err != nil {
			return err
		}

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "digitalocean",
			Operation: "get_droplet",
			Config:    map[string]any{"droplet_id": dropletID},
		})
		if err != nil {
			return err
		}
		data, err := redact.MarshalIndent(result.Data, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	},
}

var (
	serverCreateName     string
	serverCreateRegion   string
	serverCreateSize     string
	serverCreateImage    string
	serverCreateSSHKeys  []string
	serverCreateUserData string
	serverDryRun         bool
	serverAck            bool
)

var serverCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a droplet",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "digitalocean",
			Operation: "create_droplet",
			DryRun:    serverDryRun,
			Config: map[string]any{
				"name":      serverCreateName,
				"region":    serverCreateRegion,
				"size":      serverCreateSize,
				"image":     serverCreateImage,
				"ssh_keys":  serverCreateSSHKeys,
				"user_data": serverCreateUserData,
			},
		})
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), result.Data)
	},
}

var serverStartCmd = &cobra.Command{
	Use:   "start <droplet-id>",
	Short: "Power on a droplet",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServerLifecycle(cmd, args[0], "start")
	},
}

var serverStopCmd = &cobra.Command{
	Use:   "stop <droplet-id>",
	Short: "Power off a droplet",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServerLifecycle(cmd, args[0], "stop")
	},
}

var serverDestroyCmd = &cobra.Command{
	Use:   "destroy <droplet-id>",
	Short: "Destroy a droplet",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServerLifecycle(cmd, args[0], "destroy")
	},
}

func runServerLifecycle(cmd *cobra.Command, rawID, operation string) error {
	svc, closeFn, err := newExternalConnectorService(cmd.Context())
	if err != nil {
		return err
	}
	defer closeFn()

	dropletID, err := parseIntArg(rawID, "droplet id")
	if err != nil {
		return err
	}

	result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
		Connector:    "digitalocean",
		Operation:    operation,
		DryRun:       serverDryRun,
		Acknowledged: serverAck,
		Config:       map[string]any{"droplet_id": dropletID},
	})
	if err != nil {
		return err
	}
	if result.Data != nil {
		return writeJSON(cmd.OutOrStdout(), result.Data)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "DigitalOcean droplet %d %sed\n", dropletID, operation)
	return nil
}

func parseIntArg(raw, label string) (int, error) {
	var out int
	if _, err := fmt.Sscanf(raw, "%d", &out); err != nil { //nolint:govet
		return 0, fmt.Errorf("invalid %s %q: expected integer", label, raw)
	}
	return out, nil
}

func init() {
	serverCreateCmd.Flags().StringVar(&serverCreateName, "name", "", "droplet name")
	serverCreateCmd.Flags().StringVar(&serverCreateRegion, "region", "", "region slug")
	serverCreateCmd.Flags().StringVar(&serverCreateSize, "size", "", "size slug")
	serverCreateCmd.Flags().StringVar(&serverCreateImage, "image", "", "image slug")
	serverCreateCmd.Flags().StringSliceVar(&serverCreateSSHKeys, "ssh-key", nil, "SSH key fingerprint (repeatable)")
	serverCreateCmd.Flags().StringVar(&serverCreateUserData, "user-data", "", "cloud-init user-data")
	serverCreateCmd.Flags().BoolVar(&serverDryRun, "dry-run", false, "preview the operation without executing it")

	serverStopCmd.Flags().BoolVar(&serverDryRun, "dry-run", false, "preview the operation without executing it")
	serverDestroyCmd.Flags().BoolVar(&serverDryRun, "dry-run", false, "preview the operation without executing it")
	serverDestroyCmd.Flags().BoolVar(&serverAck, "ack", false, "acknowledge destructive destroy operation")

	serverCmd.AddCommand(serverListCmd)
	serverCmd.AddCommand(serverShowCmd)
	serverCmd.AddCommand(serverCreateCmd)
	serverCmd.AddCommand(serverStartCmd)
	serverCmd.AddCommand(serverStopCmd)
	serverCmd.AddCommand(serverDestroyCmd)
}
