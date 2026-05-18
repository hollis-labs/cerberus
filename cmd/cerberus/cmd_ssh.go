package main

import (
	"fmt"
	"strings"

	"github.com/chrispian/cerberus/internal/app"
	"github.com/chrispian/cerberus/internal/cerbapi"
	sshconn "github.com/chrispian/cerberus/internal/connector/ssh"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/spf13/cobra"
)

var sshCmd = &cobra.Command{
	Use:   "ssh",
	Short: "SSH operations on remote hosts",
}

var sshAcknowledge bool
var sshDryRun bool

var sshExecCmd = &cobra.Command{
	Use:   "exec <resource-id> -- <command...>",
	Short: "Execute a command on a remote host",
	Long:  "Runs a command on the remote host defined by the given resource ID.",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resourceID := args[0]

		// Find the "--" separator and extract the command after it.
		command := ""
		dashIdx := cmd.ArgsLenAtDash()
		if dashIdx >= 0 {
			command = strings.Join(args[dashIdx:], " ")
		} else if len(args) > 1 {
			command = strings.Join(args[1:], " ")
		}
		if command == "" {
			return fmt.Errorf("no command specified — use: cerberus ssh exec <resource-id> -- <command>")
		}

		a, err := app.NewWithOptions(appOptions())
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		res, err := findResource(a, resourceID)
		if err != nil {
			return err
		}

		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()
		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector:    "ssh",
			Operation:    "exec",
			Config:       sshConfig(res, command),
			DryRun:       sshDryRun,
			Acknowledged: sshAcknowledge,
		})
		if err != nil {
			return err
		}
		if sshDryRun {
			return writeJSON(cmd.OutOrStdout(), result.Data)
		}
		execResult, ok := result.Data.(*sshconn.ExecResult)
		if !ok {
			return fmt.Errorf("ssh exec: unexpected result type %T", result.Data)
		}

		if execResult.Stdout != "" {
			fmt.Println(execResult.Stdout)
		}
		if execResult.Stderr != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", execResult.Stderr)
		}
		if execResult.ExitCode != 0 {
			return fmt.Errorf("remote command exited with code %d", execResult.ExitCode)
		}
		return nil
	},
}

var sshStatusCmd = &cobra.Command{
	Use:   "status <resource-id>",
	Short: "Check SSH connectivity to a remote host",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resourceID := args[0]

		a, err := app.NewWithOptions(appOptions())
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		res, err := findResource(a, resourceID)
		if err != nil {
			return err
		}

		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()
		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "ssh",
			Operation: "status",
			Config:    sshConfig(res, ""),
		})
		if err != nil {
			return err
		}

		if statusJSON, ok := result.Data.(string); ok {
			fmt.Println(statusJSON)
			return nil
		}
		return fmt.Errorf("ssh status: unexpected result type %T", result.Data)
	},
}

var sshStopCmd = &cobra.Command{
	Use:   "stop <resource-id>",
	Short: "Shut down a remote host over SSH",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.NewWithOptions(appOptions())
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck
		res, err := findResource(a, args[0])
		if err != nil {
			return err
		}
		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()
		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector:    "ssh",
			Operation:    "stop",
			Config:       sshConfig(res, ""),
			DryRun:       sshDryRun,
			Acknowledged: sshAcknowledge,
		})
		if err != nil {
			return err
		}
		if sshDryRun {
			return writeJSON(cmd.OutOrStdout(), result.Data)
		}
		return nil
	},
}

// findResource looks up a resource by ID from the app's v2 config.
func findResource(a *app.App, id string) (*domain.Resource, error) {
	for _, r := range a.Config.Resources {
		if r.ID == id {
			return &domain.Resource{
				ID:        r.ID,
				Name:      r.Name,
				Type:      domain.ResourceType(r.Type),
				Connector: r.Connector,
				Config:    r.Config,
				Tags:      r.Tags,
				DependsOn: r.DependsOn,
			}, nil
		}
	}
	return nil, fmt.Errorf("resource %q not found", id)
}

func sshConfig(res *domain.Resource, command string) map[string]any {
	cfg := make(map[string]any, len(res.Config)+3)
	for key, value := range res.Config {
		cfg[key] = value
	}
	cfg["id"] = res.ID
	cfg["name"] = res.Name
	if command != "" {
		cfg["command"] = command
	}
	return cfg
}

func init() {
	sshExecCmd.Flags().BoolVar(&sshDryRun, "dry-run", false, "preview the remote command without executing it")
	sshExecCmd.Flags().BoolVar(&sshAcknowledge, "ack", false, "acknowledge destructive remote execution")
	sshStopCmd.Flags().BoolVar(&sshDryRun, "dry-run", false, "preview the remote shutdown without executing it")
	sshStopCmd.Flags().BoolVar(&sshAcknowledge, "ack", false, "acknowledge destructive remote execution")
	sshCmd.AddCommand(sshExecCmd)
	sshCmd.AddCommand(sshStatusCmd)
	sshCmd.AddCommand(sshStopCmd)
}
