package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/spf13/cobra"
)

var (
	// connectorsPluginDev selects a development install (devmode builds
	// only): the plugin must live under its own directory as a developer
	// root, and its destructive operations are refused. There are no trust or
	// signing flags; Cerberus does not vet plugins.
	connectorsPluginDev bool
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
		return runPluginHealth(cmd.Context(), cmd.OutOrStdout(), args[0], connectorsPluginDev)
	},
}

var connectorsPluginExecCmd = &cobra.Command{
	Use:   "exec <plugin-dir> <operation>",
	Short: "Install, load, and execute a connector plugin operation",
	Args:  cobra.ExactArgs(2),
	Long: `Install a plugin directory into a throwaway host, load it, run one
operation, and unload it. Arguments are typed from the operation's input
schema in the directory's plugin.yaml, exactly as 'cerberus connectors exec'
types them.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		spec, err := pluginhost.ReadPluginYAML(args[0])
		if err != nil {
			return err
		}
		def := contract.DefinitionFromManifest(spec.Cerberus.Connector)
		op, err := findConnectorOperation([]contract.Definition{def}, def.ID, args[1])
		if err != nil {
			return err
		}
		cfg, err := connectorExecConfig(op, connectorsPluginExecFlags, cmd.InOrStdin())
		if err != nil {
			return fmt.Errorf("%s %s: %w", def.ID, args[1], err)
		}
		return runPluginExec(cmd.Context(), cmd.OutOrStdout(), args[0], args[1], cfg, connectorsPluginExecFlags.dryRun, connectorsPluginExecFlags.ack, connectorsPluginDev)
	},
}

var connectorsPluginExecFlags connectorExecFlags

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
			Options:   pluginInstallOptions(connectorsPluginDev),
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

var connectorsPluginManagedUninstallCmd = &cobra.Command{
	Use:   "uninstall <plugin-id>",
	Short: "Remove a daemon-managed plugin, unloading it first if needed",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newManagedPluginSocketClient()
		if err != nil {
			return err
		}
		out, err := client.UninstallManagedPlugin(cmd.Context(), args[0])
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

var connectorsPluginManagedExecFlags connectorExecFlags

// managed exec is `connectors exec` restricted to installed plugins. It goes
// through the daemon's admin lane rather than calling the plugin directly, so
// dry-run, acknowledgment and redaction are the ones every other connector
// operation gets.
var connectorsPluginManagedExecCmd = &cobra.Command{
	Use:   "exec <plugin-id> <operation>",
	Short: "Execute an operation through a daemon-managed loaded plugin",
	Long: `Execute an operation on a daemon-managed plugin. This is
'cerberus connectors exec' limited to installed plugins, with the same typed
--arg, --arg-json and --input flags.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newManagedPluginSocketClient()
		if err != nil {
			return err
		}
		plugins, err := client.ListManagedPlugins(cmd.Context())
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(plugins, func(p cerbapi.ManagedPluginConnectorState) bool { return p.ID == args[0] }) {
			return fmt.Errorf("%q is not an installed plugin; run `cerberus connectors plugin managed list`, or use `cerberus connectors exec` for a built-in connector", args[0])
		}
		defs, err := client.ListConnectors(cmd.Context())
		if err != nil {
			return err
		}
		return runConnectorExec(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin(), socketConnectorExecutor{client}, defs, args[0], args[1], connectorsPluginManagedExecFlags)
	},
}

func init() {
	addPluginInstallFlags(connectorsPluginHealthCmd)
	addPluginInstallFlags(connectorsPluginExecCmd)
	addPluginInstallFlags(connectorsPluginManagedInstallCmd)
	connectorsPluginExecFlags.register(connectorsPluginExecCmd)
	connectorsPluginManagedExecFlags.register(connectorsPluginManagedExecCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedInstallCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedListCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedLoadCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedUnloadCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedUninstallCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedHealthCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedExecCmd)
	connectorsPluginCmd.AddCommand(connectorsPluginHealthCmd)
	connectorsPluginCmd.AddCommand(connectorsPluginExecCmd)
	connectorsPluginCmd.AddCommand(connectorsPluginManagedCmd)
	connectorsCmd.AddCommand(connectorsPluginCmd)
}

func addPluginInstallFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&connectorsPluginDev, "dev", false, "development install: destructive operations are refused (requires a devmode build)")
}

func runPluginHealth(ctx context.Context, out io.Writer, pluginDir string, dev bool) error {
	health, err := pluginConnectorService().Health(ctx, cerbapi.PluginConnectorHealthArgs{
		PluginDir: pluginDir,
		Options:   pluginInstallOptions(dev),
	})
	if err != nil {
		return err
	}
	return writeJSON(out, health)
}

func runPluginExec(ctx context.Context, out io.Writer, pluginDir, operation string, cfg map[string]any, dryRun, ack, dev bool) error {
	result, err := pluginConnectorService().Execute(ctx, cerbapi.PluginConnectorExecArgs{
		PluginDir:    pluginDir,
		Operation:    operation,
		Config:       cfg,
		DryRun:       dryRun,
		Acknowledged: ack,
		Options:      pluginInstallOptions(dev),
	})
	if err != nil {
		return err
	}
	return writeJSON(out, result)
}

func pluginConnectorService() *cerbapi.PluginConnectorService {
	return cerbapi.NewPluginConnectorService(version, os.Stderr,
		cerbapi.WithPluginConnectorSecrets(app.ConnectorSecrets(cfgPath)))
}

func pluginInstallOptions(dev bool) cerbapi.PluginInstallOptions {
	return cerbapi.PluginInstallOptions{DevMode: dev}
}

func newManagedPluginSocketClient() (*cerbapi.SocketClient, error) {
	path, err := cerbapi.SocketPath()
	if err != nil {
		return nil, err
	}
	return cerbapi.NewSocketClient(path), nil
}

func writeJSON(out io.Writer, value any) error {
	data, err := redact.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}
