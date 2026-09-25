package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"

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
		// As with connectors exec, an unknown operation or a bad argument is
		// a hint, not a refusal: the one-shot host refuses and records it.
		op, err := findConnectorOperation([]contract.Definition{def}, def.ID, args[1])
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "hint: %s %s: %v; sending it anyway, so the refusal is recorded\n", def.ID, args[1], err)
			op = contract.Operation{Name: args[1]}
		}
		cfg, hints, err := connectorExecConfig(op, connectorsPluginExecFlags, cmd.InOrStdin())
		if err != nil {
			return fmt.Errorf("%s %s: %w", def.ID, args[1], err)
		}
		for _, hint := range hints {
			fmt.Fprintf(cmd.ErrOrStderr(), "hint: %s %s: %s\n", def.ID, args[1], hint)
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
	Short: "Review a plugin and install it into the plugin store (interactive)",
	Long: `Review a plugin directory and install it. This runs in your terminal, not
in the daemon, and only from an interactive terminal: it prints what the plugin
declares it can do and where the declaration has gaps, and installs it only when
you type the plugin id.

The reviewed bundle is copied into ~/.cerberus/plugins/<id>/<digest>/ and runs
from there, so rebuilding the source directory changes nothing that runs until
you install it again. Installing a plugin that is already installed is an
upgrade, shown as a diff against the review you accepted before. The running
daemon is then asked to reload the plugin by id.

Every load compares the installed bundle with the one you accepted and refuses
a plugin that changed (plugin_changed); see 'load --accept-changes'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPluginReview(cmd, func(ctx context.Context, r *cerbapi.PluginReviewer) (*cerbapi.PendingReview, error) {
			return r.PrepareInstall(ctx, args[0], connectorsPluginDev)
		}, false)
	},
}

var connectorsPluginManagedReviewCmd = &cobra.Command{
	Use:   "review <plugin-id>",
	Short: "Review an installed plugin: the one-time review of a plugin installed before reviews existed (interactive)",
	Long: `Review an installed plugin and accept it by typing its id. A plugin installed
before install review existed keeps loading, reported as review_pending, until
this is run once for it: the review is the same one install shows, and
accepting it copies the plugin into the plugin store. A plugin whose bundle no
longer matches its accepted review is shown as a diff.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPluginReview(cmd, func(ctx context.Context, r *cerbapi.PluginReviewer) (*cerbapi.PendingReview, error) {
			return r.PrepareReview(ctx, args[0])
		}, false)
	},
}

var connectorsPluginAcceptChanges bool

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
	Long: `Load an installed plugin in the daemon. A plugin whose bundle no longer
matches the one you accepted is refused (plugin_changed). --accept-changes, from
an interactive terminal, shows what changed as a diff against the accepted
review, and loads the plugin once you type its id.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if connectorsPluginAcceptChanges {
			err := runPluginReview(cmd, func(ctx context.Context, r *cerbapi.PluginReviewer) (*cerbapi.PendingReview, error) {
				return r.PrepareReview(ctx, args[0])
			}, true)
			if err != nil {
				return err
			}
		}
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

func init() {
	addPluginInstallFlags(connectorsPluginHealthCmd)
	addPluginInstallFlags(connectorsPluginExecCmd)
	addPluginInstallFlags(connectorsPluginManagedInstallCmd)
	connectorsPluginExecFlags.register(connectorsPluginExecCmd)
	connectorsPluginManagedLoadCmd.Flags().BoolVar(&connectorsPluginAcceptChanges, "accept-changes", false, "review what changed since the accepted review and accept it (interactive terminal only)")
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedInstallCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedReviewCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedListCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedLoadCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedUnloadCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedUninstallCmd)
	connectorsPluginManagedCmd.AddCommand(connectorsPluginManagedHealthCmd)
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

// pluginReviewIsTerminal reports whether the review can be confirmed by a
// person: stdin and stdout are a terminal. Tests swap it.
var pluginReviewIsTerminal = func() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

var errPluginReviewNotInteractive = errors.New("a plugin install or review runs only from an interactive terminal, where it shows what the plugin can do and asks you to type its id; run it from a terminal, not a script or an agent")

// newPluginReviewer is swapped by tests.
var newPluginReviewer = func() (*cerbapi.PluginReviewer, error) { return app.NewPluginReviewer(cfgPath) }

// reloadManagedPlugin asks the daemon to re-read a reviewed entry. Swapped
// by tests.
var reloadManagedPlugin = func(ctx context.Context, id string) (cerbapi.ManagedPluginConnectorState, error) {
	client, err := newManagedPluginSocketClient()
	if err != nil {
		return cerbapi.ManagedPluginConnectorState{}, err
	}
	return client.ReloadManagedPlugin(ctx, id)
}

// runPluginReview prepares a review, shows it, takes the typed plugin id and
// accepts it, then asks the daemon to reload the plugin. quietIfNothing makes
// "nothing to review" a success, for load --accept-changes on a plugin that
// has not changed.
func runPluginReview(cmd *cobra.Command, prepare func(context.Context, *cerbapi.PluginReviewer) (*cerbapi.PendingReview, error), quietIfNothing bool) error {
	if !pluginReviewIsTerminal() {
		return errPluginReviewNotInteractive
	}
	ctx := inProcessContext(cmd.Context())
	out := cmd.OutOrStdout()
	reviewer, err := newPluginReviewer()
	if err != nil {
		return err
	}
	pending, err := prepare(ctx, reviewer)
	if errors.Is(err, cerbapi.ErrNothingToReview) {
		if quietIfNothing {
			return nil
		}
		_, err = fmt.Fprintln(out, redact.Text(err.Error()))
		return err
	}
	if err != nil {
		return err
	}
	_, _ = fmt.Fprint(out, pending.Text())
	_, _ = fmt.Fprint(out, "\n"+pending.Prompt())
	line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		reviewer.Discard(pending)
		return readErr
	}
	state, err := reviewer.Accept(ctx, pending, line)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Accepted %s %s (%s); it runs from %s\n", state.ID, state.Version, state.BundleDigest, state.Path)
	reloaded, err := reloadManagedPlugin(cmd.Context(), state.ID)
	var unreachable *cerbapi.DaemonUnreachableError
	switch {
	case errors.As(err, &unreachable):
		_, err = fmt.Fprintln(out, "The daemon is not running; it picks the reviewed plugin up when it starts.")
		return err
	case err != nil:
		return fmt.Errorf("the review was accepted and recorded, but the daemon did not reload %q: %w", state.ID, err)
	}
	if reloaded.Loaded {
		_, err = fmt.Fprintf(out, "The daemon reloaded %s.\n", state.ID)
	} else {
		_, err = fmt.Fprintf(out, "The daemon has registered %s; load it with `cerberus connectors plugin managed load %s`.\n", state.ID, state.ID)
	}
	return err
}

func pluginConnectorService() *cerbapi.PluginConnectorService {
	return app.NewPluginConnectorService(version, os.Stderr, cfgPath)
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
