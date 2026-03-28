package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/chrispian/cerberus/internal/app"
	sshconn "github.com/chrispian/cerberus/internal/connector/ssh"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/spf13/cobra"
)

var sshCmd = &cobra.Command{
	Use:   "ssh",
	Short: "SSH operations on remote hosts",
}

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

		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		res, err := findResource(a, resourceID)
		if err != nil {
			return err
		}

		conn := sshconn.New(a.Secrets)
		result, err := conn.Exec(context.Background(), res, command)
		if err != nil {
			return err
		}

		if result.Stdout != "" {
			fmt.Println(result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", result.Stderr)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("remote command exited with code %d", result.ExitCode)
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

		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		res, err := findResource(a, resourceID)
		if err != nil {
			return err
		}

		conn := sshconn.New(a.Secrets)
		statusJSON, err := conn.HostStatusJSON(context.Background(), res)
		if err != nil {
			return err
		}

		fmt.Println(statusJSON)
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

func init() {
	sshCmd.AddCommand(sshExecCmd)
	sshCmd.AddCommand(sshStatusCmd)
}
