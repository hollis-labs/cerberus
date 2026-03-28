package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	dockerconn "github.com/chrispian/cerberus/internal/connector/docker"
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
		dc, err := dockerconn.New()
		if err != nil {
			return err
		}

		containers, err := dc.ListContainers(context.Background())
		if err != nil {
			return err
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
		dc, err := dockerconn.New()
		if err != nil {
			return err
		}

		logs, err := dc.Logs(context.Background(), args[0], dockerLogsLines)
		if err != nil {
			return err
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
		dc, err := dockerconn.New()
		if err != nil {
			return err
		}

		resourceID := args[0]
		ctx := context.Background()

		// Check if a compose file was specified
		composeFile, _ := cmd.Flags().GetString("file")
		if composeFile != "" {
			if composeErr := dc.ComposeUp(ctx, composeFile); composeErr != nil {
				return composeErr
			}
			fmt.Printf("Compose stack started: %s\n", composeFile)
			return nil
		}

		// Default: treat resource-id as container name
		if startErr := dc.StartContainer(ctx, resourceID); startErr != nil {
			return startErr
		}

		// Show status after start
		status, err := dc.ContainerStatus(ctx, resourceID)
		if err == nil {
			data, _ := json.MarshalIndent(status, "", "  ")
			fmt.Println(string(data))
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
		dc, err := dockerconn.New()
		if err != nil {
			return err
		}

		resourceID := args[0]
		ctx := context.Background()

		// Check if a compose file was specified
		composeFile, _ := cmd.Flags().GetString("file")
		if composeFile != "" {
			if err := dc.ComposeDown(ctx, composeFile); err != nil {
				return err
			}
			fmt.Printf("Compose stack stopped: %s\n", composeFile)
			return nil
		}

		// Default: treat resource-id as container name
		if err := dc.StopContainer(ctx, resourceID); err != nil {
			return err
		}
		fmt.Printf("Container stopped: %s\n", resourceID)
		return nil
	},
}

func init() {
	dockerLogsCmd.Flags().IntVar(&dockerLogsLines, "lines", 50, "number of log lines to show")
	dockerUpCmd.Flags().StringP("file", "f", "", "compose file path (for compose mode)")
	dockerDownCmd.Flags().StringP("file", "f", "", "compose file path (for compose mode)")
	dockerCmd.AddCommand(dockerPSCmd)
	dockerCmd.AddCommand(dockerLogsCmd)
	dockerCmd.AddCommand(dockerUpCmd)
	dockerCmd.AddCommand(dockerDownCmd)
}
