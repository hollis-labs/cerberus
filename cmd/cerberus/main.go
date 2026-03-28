package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/service"
	"github.com/chrispian/cerberus/internal/tui"
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

// rootCmd launches the TUI when no subcommand is given.
var rootCmd = &cobra.Command{
	Use:   "cerberus",
	Short: "Fragments Engine service manager",
	Long: `Cerberus — agent-first local service manager for the Fragments Engine ecosystem.

Manage, monitor, and protect your dev services from a single TUI,
CLI, or MCP server. Prevents agents from clobbering each other's
servers with PID tracking, port conflict detection, and service locks.

(c) HOLLIS LABS`,
	Version: version,
	RunE:    runTUI,
}

func init() {
	rootCmd.SetVersionTemplate(fmt.Sprintf("cerberus %s (built %s)\n(c) HOLLIS LABS\n", version, buildDate))
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", config.DefaultPath(), "path to config file")

	rootCmd.AddCommand(upCmd)
	rootCmd.AddCommand(downCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(buildCmd)
	rootCmd.AddCommand(rebuildCmd)
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(uninstallCmd)
	rootCmd.AddCommand(pauseCmd)
	rootCmd.AddCommand(resumeCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(projectCmd)
	rootCmd.AddCommand(resourceCmd)
	rootCmd.AddCommand(pipelineCmd)
	rootCmd.AddCommand(githubCmd)
	rootCmd.AddCommand(serverCmd)
}

// runTUI launches the interactive Bubble Tea TUI (default behavior).
func runTUI(cmd *cobra.Command, args []string) error {
	service.InitLifecycleLog()

	// Auto-create config on first run
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) && cfgPath == config.DefaultPath() {
		if err := config.EnsureDefault(); err != nil {
			return fmt.Errorf("error creating config: %w", err)
		}
		fmt.Printf("Created default config at %s\n", config.DefaultPath())
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	services := service.NewFromConfig(cfg)
	m := tui.NewModel(services, version, buildDate)

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	return nil
}

// loadServices loads config and creates service objects.
func loadServices() ([]*service.ManagedService, error) {
	service.InitLifecycleLog()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	return service.NewFromConfig(cfg), nil
}

// filterServices returns services matching the given IDs or tag.
// If no IDs and no tag, returns all services.
func filterServices(services []*service.ManagedService, ids []string, tag string) []*service.ManagedService {
	if len(ids) == 0 && tag == "" {
		return services
	}

	var result []*service.ManagedService
	for _, svc := range services {
		if matchesFilter(svc, ids, tag) {
			result = append(result, svc)
		}
	}
	return result
}

func matchesFilter(svc *service.ManagedService, ids []string, tag string) bool {
	if tag != "" {
		for _, t := range svc.Def.Tags {
			if strings.EqualFold(t, tag) {
				return true
			}
		}
	}
	for _, id := range ids {
		if strings.EqualFold(svc.Def.ID, id) {
			return true
		}
	}
	return false
}
