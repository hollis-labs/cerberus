package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	"github.com/spf13/cobra"
)

var dockerCmd = &cobra.Command{
	Use:   "docker",
	Short: "Docker operations",
}

var dockerPSCmd = &cobra.Command{
	Use:   "ps",
	Short: "List running containers",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "docker",
			Operation: "list_containers",
			Config:    dockerTargetConfig(cmd, nil),
		})
		if err != nil {
			return err
		}
		containers, ok := result.Data.([]dockerconn.Container)
		if !ok {
			return fmt.Errorf("docker ps: unexpected result type %T", result.Data)
		}

		if len(containers) == 0 {
			fmt.Println("No running containers.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tIMAGE\tSTATUS\tPORTS")
		fmt.Fprintln(w, "--\t----\t-----\t------\t-----")
		for _, c := range containers {
			ports := ""
			if len(c.Ports) > 0 {
				ports = c.Ports[0]
				if len(c.Ports) > 1 {
					ports += fmt.Sprintf(" (+%d)", len(c.Ports)-1)
				}
			}
			fmt.Fprintf(w, "%.12s\t%s\t%s\t%s\t%s\n",
				c.ID, c.Name, c.Image, c.Status, ports)
		}
		return w.Flush()
	},
}

var dockerLogsLines int

var dockerLogsCmd = &cobra.Command{
	Use:   "logs <container>",
	Short: "Show container logs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "docker",
			Operation: "logs",
			Config: dockerTargetConfig(cmd, map[string]any{
				"container": args[0],
				"lines":     dockerLogsLines,
			}),
		})
		if err != nil {
			return err
		}
		logs, ok := result.Data.(string)
		if !ok {
			return fmt.Errorf("docker logs: unexpected result type %T", result.Data)
		}

		fmt.Print(logs)
		return nil
	},
}

var dockerUpCmd = &cobra.Command{
	Use:   "up <resource-id>",
	Short: "Start container or compose stack",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()

		resourceID := args[0]
		composeFile, _ := cmd.Flags().GetString("file")
		cfg := dockerTargetConfig(cmd, dockerResourceConfig(resourceID, composeFile))

		if _, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "docker",
			Operation: "start",
			Config:    cfg,
		}); err != nil {
			return err
		}

		if composeFile != "" {
			fmt.Printf("Compose stack started: %s\n", composeFile)
			return nil
		}
		fmt.Printf("Container started: %s\n", resourceID)
		return nil
	},
}

var dockerDownCmd = &cobra.Command{
	Use:   "down <resource-id>",
	Short: "Stop container or compose stack",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()

		resourceID := args[0]
		composeFile, _ := cmd.Flags().GetString("file")
		cfg := dockerTargetConfig(cmd, dockerResourceConfig(resourceID, composeFile))

		if _, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "docker",
			Operation: "stop",
			Config:    cfg,
		}); err != nil {
			return err
		}

		if composeFile != "" {
			fmt.Printf("Compose stack stopped: %s\n", composeFile)
			return nil
		}

		fmt.Printf("Container stopped: %s\n", resourceID)
		return nil
	},
}

// dockerTargetConfig folds the --host/--context flags into an operation's
// config. Host selection is a property of the call, not of the process, so it
// travels in the payload rather than in the CLI's own environment — the
// operation runs in the daemon, whose environment is not this shell's.
func dockerTargetConfig(cmd *cobra.Command, cfg map[string]any) map[string]any {
	host, _ := cmd.Flags().GetString("host")
	dockerContext, _ := cmd.Flags().GetString("context")
	if host == "" && dockerContext == "" {
		return cfg
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	if host != "" {
		cfg["host"] = host
	}
	if dockerContext != "" {
		cfg["context"] = dockerContext
	}
	return cfg
}

func dockerResourceConfig(resourceID, composeFile string) map[string]any {
	cfg := map[string]any{
		"id":        resourceID,
		"name":      resourceID,
		"container": resourceID,
	}
	if composeFile != "" {
		cfg["compose_file"] = composeFile
	}
	return cfg
}

func init() {
	dockerLogsCmd.Flags().IntVar(&dockerLogsLines, "lines", 50, "number of log lines to show")
	dockerUpCmd.Flags().StringP("file", "f", "", "compose file path (for compose mode)")
	dockerDownCmd.Flags().StringP("file", "f", "", "compose file path (for compose mode)")
	for _, sub := range []*cobra.Command{dockerPSCmd, dockerLogsCmd, dockerUpCmd, dockerDownCmd} {
		sub.Flags().StringP("host", "H", "", "Docker daemon to target as a DOCKER_HOST value (ssh://user@host, tcp://host:2376)")
		sub.Flags().String("context", "", "Docker context name to target (mutually exclusive with --host)")
		dockerCmd.AddCommand(sub)
	}
}
