package main

import (
	"fmt"
	"os"

	"github.com/chrispian/cerberus/internal/app"
	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/registry"
	"github.com/spf13/cobra"
)

var (
	cfgPath string
	// dbPath is the value of the persistent --db flag. Empty means the main
	// database path is resolved via go-apppaths (XDG mode), which still
	// honors CERBERUS_DB_PATH / CERBERUS_WORKSPACE natively. A non-empty
	// value overrides that resolution.
	dbPath string

	// Set via -ldflags at build time
	version   = "0.3.0"
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
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "cerberus",
	Short: "Agent-first local infrastructure manager",
	Long: `Cerberus by Hollis Labs — agent-first local infrastructure manager.

Cerberus is now v2-only for local workload management.
Use the resource commands for active local process management, especially
os_service and artifact-backed runtime management.

For day-to-day operations, start with:
- cerberus resource list
- cerberus resource status <id>
- cerberus resource deploy <id>   # build + apply when source changed
- cerberus resource apply <id>    # apply only when the right artifact already exists
- cerberus resource reload <id>   # restart the installed service only
- cerberus resource sync <id>     # copy artifact without touching runtime backend
- cerberus resource stop <id>     # stop without deleting install state
- cerberus resource doctor <id>   # use when apply or deploy fails
- cerberus resource logs <id>
- cerberus resource remove <id>   # uninstall runtime state; not a casual stop
- cerberus web                    # compact local web console

The Cerberus daemon itself now also fits this model as the v2 local process
resource "cerberus-daemon-service" on macOS launchd.

MIT licensed. Published by Hollis Labs.`,
	Version: version,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	rootCmd.SetVersionTemplate(fmt.Sprintf("cerberus %s (built %s)\nCerberus by Hollis Labs\nMIT licensed\n", version, buildDate))
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
	serverCmd.GroupID = "platform"
	sshCmd.GroupID = "platform"
	domainCmd.GroupID = "platform"
	dnsCmd.GroupID = "platform"
	forgeCmd.GroupID = "platform"
	cloudflareCmd.GroupID = "platform"
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
	rootCmd.AddCommand(serverCmd)
	rootCmd.AddCommand(sshCmd)
	rootCmd.AddCommand(domainCmd)
	rootCmd.AddCommand(dnsCmd)
	rootCmd.AddCommand(forgeCmd)
	rootCmd.AddCommand(cloudflareCmd)
	rootCmd.AddCommand(dockerCmd)
	rootCmd.AddCommand(connectorsCmd)
}

// loadUnifiedForTools resolves the effective config for the standalone
// `cerberus mcp` subprocess's few remaining connector-based tools (SSH)
// that read config locally. It resolves the registry the same way the
// daemon does, so the MCP subprocess sees registered project configs.
// All service-lifecycle tools route through the daemon socket instead.
func loadUnifiedForTools(path string) (*config.ConfigV2, error) {
	return registry.ResolveConfig(path)
}
