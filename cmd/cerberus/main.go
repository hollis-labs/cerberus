package main

import (
	"fmt"
	"os"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/spf13/cobra"
)

var (
	cfgPath string

	// Set via -ldflags at build time
	version   = "0.3.0"
	buildDate = "unknown"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "cerberus",
	Short: "Agent-first local infrastructure manager",
	Long: `Cerberus — agent-first local infrastructure manager for the Fragments Engine ecosystem.

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

(c) HOLLIS LABS`,
	Version: version,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	rootCmd.SetVersionTemplate(fmt.Sprintf("cerberus %s (built %s)\n(c) HOLLIS LABS\n", version, buildDate))
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", config.DefaultPath(), "path to config file")

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

	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(webCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(uninstallCmd)
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
}

// loadUnifiedForTools is a thin wrapper around config.LoadUnified used
// by the standalone `cerberus mcp` subprocess for the few remaining
// connector-based tools (SSH) that read config locally. All
// service-lifecycle tools route through the daemon socket instead.
func loadUnifiedForTools(path string) (*config.ConfigV2, error) {
	return config.LoadUnified(path)
}
