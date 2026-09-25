package main

import (
	"fmt"
	"os"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/spf13/cobra"
)

var (
	cfgPath string
	// dbPath is the value of the persistent --db flag. Empty means the main
	// database path is resolved via go-apppaths (XDG mode), which still
	// honors CERBERUS_DB_PATH / CERBERUS_WORKSPACE natively. A non-empty
	// value overrides that resolution.
	dbPath string

	// Set via -ldflags at build time. See Makefile for stamping.
	version   = "0.4.0"
	commit    = "unknown"
	buildDate = "unknown"
)

// appOptions builds the app.Options the cobra command tree resolves from the
// persistent --config / --db flags. Call sites use app.NewWithOptions so the
// --db override threads through to go-apppaths.
func appOptions() app.Options {
	return app.Options{ConfigPath: cfgPath, DBPath: dbPath}
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", redact.Text(err.Error()))
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:           "cerberus",
	SilenceErrors: true,
	Short:         "Agent-first local infrastructure manager",
	Long: `Cerberus by Hollis Labs — agent-first local infrastructure manager.

Cerberus is now v2-only for local workload management.
Use the resource commands for active local process management, especially
os_service and artifact-backed runtime management.

For day-to-day operations, start with:
- cerberus resource list
- cerberus resource status <id>
- cerberus resource deploy <id> --ack   # build + apply when source changed
- cerberus resource apply <id> --ack    # apply only when the right artifact already exists
- cerberus resource reload <id> --ack   # restart the installed service only
- cerberus resource sync <id> --ack     # copy artifact without touching runtime backend
- cerberus resource stop <id> --ack     # stop without deleting install state
- cerberus resource doctor <id>         # use when apply or deploy fails
- cerberus resource logs <id>
- cerberus resource remove <id> --ack   # uninstall runtime state; not a casual stop

Every resource mutation and pipeline run needs --ack: each is a lifecycle,
write, destructive or exec operation.
- cerberus web                    # compact local web console

The Cerberus daemon itself now also fits this model as the v2 local process
resource "cerberus-daemon-service" on macOS launchd.

MIT licensed. Published by Hollis Labs.`,
	Version: version,
	// By the time a pre-run hook runs, cobra has parsed the flags and checked
	// the arguments, so misuse has already been reported with its usage. A
	// failure after this point is operational — a refusal, a missing
	// credential, a provider error — and a usage block under it only buries
	// the message that says what to do.
	PersistentPreRun: func(cmd *cobra.Command, _ []string) {
		cmd.SilenceUsage = true
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	rootCmd.SetVersionTemplate(fmt.Sprintf("cerberus %s (commit %s, built %s)\nCerberus by Hollis Labs\nMIT licensed\n", version, commit, buildDate))
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", config.DefaultPath(), "path to config file")
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", "", "override the main database path (default: go-apppaths XDG resolution; CERBERUS_DB_PATH is also honored)")

	rootCmd.AddGroup(
		&cobra.Group{ID: "resources", Title: "V2 Resource Commands"},
		&cobra.Group{ID: "runtime", Title: "Daemon And Runtime Commands"},
		&cobra.Group{ID: "platform", Title: "Platform And Connector Commands"},
	)

	validateCmd.GroupID = "runtime"
	initCmd.GroupID = "runtime"
	daemonCmd.GroupID = "runtime"
	webCmd.GroupID = "runtime"
	mcpCmd.GroupID = "runtime"
	installCmd.GroupID = "runtime"
	uninstallCmd.GroupID = "runtime"
	pathCommand.GroupID = "runtime"

	projectCmd.GroupID = "resources"
	resourceCmd.GroupID = "resources"
	pipelineCmd.GroupID = "resources"

	githubCmd.GroupID = "platform"
	sshCmd.GroupID = "platform"
	dockerCmd.GroupID = "platform"
	connectorsCmd.GroupID = "platform"

	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(registerCmd)
	rootCmd.AddCommand(deregisterCmd)
	rootCmd.AddCommand(registryCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(webCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(mcpHTTPCmd)
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(uninstallCmd)
	rootCmd.AddCommand(pathCommand)
	rootCmd.AddCommand(projectCmd)
	rootCmd.AddCommand(resourceCmd)
	rootCmd.AddCommand(pipelineCmd)
	rootCmd.AddCommand(githubCmd)
	rootCmd.AddCommand(sshCmd)
	rootCmd.AddCommand(dockerCmd)
	rootCmd.AddCommand(connectorsCmd)
	rootCmd.AddCommand(runSecretsCmd)
}

// loadUnifiedForTools resolves the effective config for the standalone
// `cerberus mcp` subprocess's few remaining connector-based tools (SSH)
// that read config locally. It resolves the registry the same way the
// daemon does, so the MCP subprocess sees registered project configs.
// All service-lifecycle tools route through the daemon socket instead.
func loadUnifiedForTools(path string) (*config.ConfigV2, error) {
	return registry.ResolveConfig(path)
}
