package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/spf13/cobra"
)

type pluginTrustOptions struct {
	devMode       bool
	catalogSigned bool
	archiveSigned bool
	archiveSHA256 string
}

var (
	connectorsPluginTrust pluginTrustOptions
	connectorsPluginArgs  []string
	connectorsPluginDry   bool
	connectorsPluginAck   bool
)

var connectorsPluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Run connector plugins through the local plugin host",
}

var connectorsPluginHealthCmd = &cobra.Command{
	Use:   "health <plugin-dir>",
	Short: "Install, load, and health-check a plugin directory",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPluginHealth(cmd.Context(), cmd.OutOrStdout(), args[0], connectorsPluginTrust)
	},
}

var connectorsPluginExecCmd = &cobra.Command{
	Use:   "exec <plugin-dir> <operation>",
	Short: "Install, load, and execute a connector plugin operation",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		parsedArgs, err := parsePluginArgs(connectorsPluginArgs)
		if err != nil {
			return err
		}
		return runPluginExec(cmd.Context(), cmd.OutOrStdout(), args[0], args[1], parsedArgs, connectorsPluginDry, connectorsPluginTrust)
	},
}

var connectorsPluginManagedCmd = &cobra.Command{
	Use:   "managed",
	Short: "Drive daemon-managed plugin lifecycle over the Cerberus socket",
}

var connectorsPluginManagedInstallCmd = &cobra.Command{
	Use:   "install <plugin-dir>",
	Short: "Register a plugin directory with the daemon-managed plugin host",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newManagedPluginSocketClient()
		if err != nil {
			return err
		}
		out, err := client.InstallManagedPlugin(cmd.Context(), cerbapi.PluginConnectorHealthArgs{
			PluginDir: args[0],
			Trust:     pluginTrust(connectorsPluginTrust),
		})
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), out)
	},
}

var connectorsPluginManagedListCmd = &cobra.Command{
	Use:   "list",
	Short: "List daemon-managed plugins",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newManagedPluginSocketClient()
		if err != nil {
			return err
		}
		out, err := client.ListManagedPlugins(cmd.Context())
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), out)
	},
}

var connectorsPluginManagedLoadCmd = &cobra.Command{
	Use:   "load <plugin-id>",
	Short: "Load a daemon-managed plugin",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newManagedPluginSocketClient()
		if err != nil {
			return err
		}
		out, err := client.LoadManagedPlugin(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), out)
	},
}

var connectorsPluginManagedUnloadCmd = &cobra.Command{
	Use:   "unload <plugin-id>",
	Short: "Unload a daemon-managed plugin",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newManagedPluginSocketClient()
		if err != nil {
			return err
		}
		out, err := client.UnloadManagedPlugin(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), out)
	},
}

var connectorsPluginManagedHealthCmd = &cobra.Command{
	Use:   "health <plugin-id>",
	Short: "Check health of a daemon-managed plugin",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newManagedPluginSocketClient()
		if err != nil {
			return err
		}
		out, err := client.ManagedPluginHealth(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), out)
	},
}

var connectorsPluginManagedExecCmd = &cobra.Command{
	Use:   "exec <plugin-id> <operation>",
	Short: "Execute an operation through a daemon-managed loaded plugin",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newManagedPluginSocketClient()
		if err != nil {
			return err
		}
		parsedArgs, err := parsePluginArgs(connectorsPluginArgs)
		if err != nil {
			return err
		}
		out, err := client.ExecuteManagedPlugin(cmd.Context(), args[0], cerbapi.PluginConnectorExecArgs{
			Operation:    args[1],
			Config:       parsedArgs,
			DryRun:       connectorsPluginDry,
			Acknowledged: connectorsPluginAck,
		})
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), out)
	},
}

func init() {
	addPluginTrustFlags(connectorsPluginHealthCmd)
	addPluginTrustFlags(connectorsPluginExecCmd)
	addPluginTrustFlags(connectorsPluginManagedInstallCmd)
	connectorsPluginExecCmd.Flags().StringArrayVar(&connectorsPluginArgs, "arg", nil, "operation argument in key=value form")
	connectorsPluginExecCmd.Flags().BoolVar(&connectorsPluginDry, "dry-run", false, "request dry-run execution when supported")
	connectorsPluginExecCmd.Flags().BoolVar(&connectorsPluginAck, "ack", false, "acknowledge destructive plugin operation")
	connectorsPluginManagedExecCmd.Flags().StringArrayVar(&connectorsPluginArgs, "arg", nil, "operation argument in key=value form")
	connectorsPluginManagedExecCmd.Flags().BoolVar(&connectorsPluginDry, "dry-run", false, "request dry-run execution when supported")
	connectorsPluginManagedExecCmd.Flags().BoolVar(&connectorsPluginAck, "ack", false, "acknowledge destructive plugin operation")
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedInstallCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedListCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedLoadCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedUnloadCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedHealthCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedExecCmd)
	connectorsPluginCmd.AddCommand(connectorsPluginHealthCmd)
	connectorsPluginCmd.AddCommand(connectorsPluginExecCmd)
	connectorsPluginCmd.AddCommand(connectorsPluginManagedCmd)
	connectorsCmd.AddCommand(connectorsPluginCmd)
}

func addPluginTrustFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&connectorsPluginTrust.devMode, "dev", false, "use developer trust mode for unsigned local plugins (requires devmode build)")
	cmd.Flags().BoolVar(&connectorsPluginTrust.catalogSigned, "catalog-signed", false, "treat the local plugin source as catalog-signed")
	cmd.Flags().BoolVar(&connectorsPluginTrust.archiveSigned, "archive-signed", false, "treat the local plugin source as archive-signed")
	cmd.Flags().StringVar(&connectorsPluginTrust.archiveSHA256, "archive-sha256", "", "recorded archive sha256 for the plugin artifact")
}

func runPluginHealth(ctx context.Context, out io.Writer, pluginDir string, trust pluginTrustOptions) error {
	health, err := pluginConnectorService().Health(ctx, cerbapi.PluginConnectorHealthArgs{
		PluginDir: pluginDir,
		Trust:     pluginTrust(trust),
	})
	if err != nil {
		return err
	}
	return writeJSON(out, health)
}

func runPluginExec(ctx context.Context, out io.Writer, pluginDir, operation string, cfg map[string]any, dryRun bool, trust pluginTrustOptions) error {
	result, err := pluginConnectorService().Execute(ctx, cerbapi.PluginConnectorExecArgs{
		PluginDir:    pluginDir,
		Operation:    operation,
		Config:       cfg,
		DryRun:       dryRun,
		Acknowledged: connectorsPluginAck,
		Trust:        pluginTrust(trust),
	})
	if err != nil {
		return err
	}
	return writeJSON(out, result)
}

func pluginConnectorService() *cerbapi.PluginConnectorService {
	return cerbapi.NewPluginConnectorService(version, nil)
}

func pluginTrust(trust pluginTrustOptions) cerbapi.PluginConnectorTrustOptions {
	return cerbapi.PluginConnectorTrustOptions{
		DevMode:       trust.devMode,
		CatalogSigned: trust.catalogSigned,
		ArchiveSigned: trust.archiveSigned,
		ArchiveSHA256: trust.archiveSHA256,
	}
}

func newManagedPluginSocketClient() (*cerbapi.SocketClient, error) {
	path, err := cerbapi.SocketPath()
	if err != nil {
		return nil, err
	}
	return cerbapi.NewSocketClient(path), nil
}

func parsePluginArgs(items []string) (map[string]any, error) {
	cfg := make(map[string]any, len(items))
	for _, item := range items {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --arg %q, want key=value", item)
		}
		cfg[key] = value
	}
	return cfg, nil
}

func writeJSON(out io.Writer, value any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
