// Command cerberus is the Cerberus control plane, built on Substrate. The shell (config,
// logging, health, HTTP host, database, CLI root) comes from the manifest; this package and
// internal/ are the app. The port lands in slices (docs/substrate-port-plan.md, section 11);
// this is slice 1, the skeleton.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/substrate/boot"
	"github.com/hollis-labs/substrate/modules/cli"
	"github.com/hollis-labs/substrate/modules/database"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/storage"
)

func main() {
	// The plugin shim: set a plugin's rlimits and exec it. It runs before anything else, so no
	// config, data directory or database is touched on the way to the plugin.
	if len(os.Args) > 1 && os.Args[1] == pluginhost.PluginExecCommand {
		err := pluginhost.RunPluginExec(os.Args[2:], os.Environ())
		fmt.Fprintln(os.Stderr, "cerberus:", err)
		os.Exit(127)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cerberus:", err)
		stop()
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, args []string) error {
	// The database module applies the embedded schema when it opens the file.
	database.UseMigrationFS(storage.Migrations())
	modules, opts := substrateModules, bootOptions(getenv)
	switch {
	case isStdioMCP(args):
		// `cerberus mcp` forwards to the serving Cerberus: it opens and creates nothing, and
		// starts only what stdio MCP needs (auth for the local principal's scopes, mcp).
		modules, opts = pickModules(stdioModules), boot.Options{}
	case !needsData(args):
		// Help, version, config and the read verbs open and create nothing: no data directory,
		// no database, no signing key. Only the modules that read config start.
		modules, opts = pickModules(readOnlyModules), boot.Options{}
	}
	started, err := boot.StartWith(ctx, substrateApp, modules, substrateConfigJSON, opts)
	if err != nil {
		return err
	}
	defer func() { _ = started.Stop(context.Background()) }()

	c := &cerberus{app: started.App, home: home(getenv)}
	rootCmd := cli.Root(started.App)
	serve := c.serveCmd(ctx)
	// `cerberus` with no command serves, like `cerberus serve`.
	rootCmd.RunE = func(cmd *cobra.Command, args []string) error { return serve.RunE(cmd, args) }
	rootCmd.Args = cobra.NoArgs
	rootCmd.AddCommand(serve, auditCmd(), c.mcpCmd(ctx))
	rootCmd.AddCommand(c.readCommands()...)
	rootCmd.AddCommand(c.brakeCommands()...)
	rootCmd.AddCommand(c.pluginCommands()...)
	rootCmd.AddCommand(c.runSecretsCmd())
	guardKeyCreate(rootCmd)
	return rootCmd.ExecuteContext(ctx)
}

// guardKeyCreate refuses `cerberus identity key create` with a scope no machine credential may
// hold (api.RefusedKeyScope): the offline path gets the same rule as the HTTP one.
func guardKeyCreate(root *cobra.Command) {
	create, _, err := root.Find([]string{"identity", "key", "create"})
	if err != nil || create == nil || create.Name() != "create" {
		return
	}
	prev := create.PreRunE
	create.PreRunE = func(cmd *cobra.Command, args []string) error {
		scopes, _ := cmd.Flags().GetStringSlice("scope")
		for _, s := range scopes {
			if why := api.RefusedKeyScope(s); why != "" {
				return errors.New(why)
			}
		}
		if prev != nil {
			return prev(cmd, args)
		}
		return nil
	}
}

// readOnlyModules open and create no file.
var readOnlyModules = []string{"config", "logging", "health", "cli"}

// stdioModules are `cerberus mcp`'s: no database, data directory, signing key or loop.
var stdioModules = []string{"config", "logging", "auth", "mcp", "cli"}

// isStdioMCP reports whether the command line is `cerberus mcp` (not its help).
func isStdioMCP(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "--version" {
			return false
		}
	}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a == "mcp"
		}
	}
	return false
}

// needsData reports whether the command line runs a command bound to the data (the database,
// the signing key, the data directory): serve (also the default with no command) and the offline
// identity commands. Help anywhere on the line, version, config, health, completion, the read
// verbs (they call the serving Cerberus over HTTP), run-secrets (a managed service's start, which
// must not open the database; whatever follows its "--" is the service's command line) and
// unknown words do not. Every argument that
// does not start with "-" counts as a command word, which holds while the root has no global flag
// taking a value (TestRootHasNoValueFlags).
func needsData(args []string) bool {
	var words []string
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "--version" {
			return false
		}
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
	}
	switch {
	case len(words) == 0 || words[0] == "serve":
		return true
	case words[0] == "identity" && len(words) > 1 && (words[1] == "setup" || (words[1] == "key" && len(words) > 2 && words[2] == "create")):
		return true
	}
	return false
}

// pickModules keeps the start order of substrateModules.
func pickModules(want []string) []string {
	var out []string
	for _, m := range substrateModules {
		if slices.Contains(want, m) {
			out = append(out, m)
		}
	}
	return out
}
