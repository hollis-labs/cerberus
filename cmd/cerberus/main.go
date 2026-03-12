package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/daemon"
	"github.com/chrispian/cerberus/internal/mcp"
	"github.com/chrispian/cerberus/internal/service"
	"github.com/chrispian/cerberus/internal/tui"
	"github.com/spf13/cobra"
)

var (
	cfgPath string

	// Set via -ldflags at build time
	version   = "0.2.0"
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
	Short: "Tiamat service manager",
	Long: `Cerberus — agent-first local service manager for the Tiamat ecosystem.

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
func loadServices() ([]*service.Service, error) {
	service.InitLifecycleLog()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	return service.NewFromConfig(cfg), nil
}

// filterServices returns services matching the given IDs or tag.
// If no IDs and no tag, returns all services.
func filterServices(services []*service.Service, ids []string, tag string) []*service.Service {
	if len(ids) == 0 && tag == "" {
		return services
	}

	var result []*service.Service
	for _, svc := range services {
		if matchesFilter(svc, ids, tag) {
			result = append(result, svc)
		}
	}
	return result
}

func matchesFilter(svc *service.Service, ids []string, tag string) bool {
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

// --- up ---

var upTag string

var upCmd = &cobra.Command{
	Use:   "up [service...]",
	Short: "Start services headlessly",
	Long:  "Starts specified services (or all if none given). Use --tag to filter by tag.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		targets := filterServices(services, args, upTag)
		if len(targets) == 0 {
			fmt.Println("No matching services found.")
			return nil
		}

		for _, svc := range targets {
			svc.Poll()
			if svc.Status == service.StatusRunning {
				fmt.Printf("%-20s already running (pid %d)\n", svc.Def.ID, svc.PID)
				continue
			}
			if err := svc.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "%-20s error: %v\n", svc.Def.ID, err)
			} else {
				fmt.Printf("%-20s starting...\n", svc.Def.ID)
			}
		}
		return nil
	},
}

func init() {
	upCmd.Flags().StringVar(&upTag, "tag", "", "filter services by tag")
}

// --- down ---

var downTag string

var downCmd = &cobra.Command{
	Use:   "down [service...]",
	Short: "Stop services headlessly",
	Long:  "Stops specified services (or all if none given). Use --tag to filter by tag.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		targets := filterServices(services, args, downTag)
		if len(targets) == 0 {
			fmt.Println("No matching services found.")
			return nil
		}

		for _, svc := range targets {
			svc.Poll()
			if svc.Status == service.StatusStopped {
				fmt.Printf("%-20s already stopped\n", svc.Def.ID)
				continue
			}
			if err := svc.Stop(); err != nil {
				fmt.Fprintf(os.Stderr, "%-20s error: %v\n", svc.Def.ID, err)
			} else {
				fmt.Printf("%-20s stopping...\n", svc.Def.ID)
			}
		}
		return nil
	},
}

func init() {
	downCmd.Flags().StringVar(&downTag, "tag", "", "filter services by tag")
}

// --- restart ---

var restartTag string

var restartCmd = &cobra.Command{
	Use:   "restart [service...]",
	Short: "Restart services headlessly",
	Long:  "Stops then starts specified services (or all if none given). Use --tag to filter by tag.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		targets := filterServices(services, args, restartTag)
		if len(targets) == 0 {
			fmt.Println("No matching services found.")
			return nil
		}

		for _, svc := range targets {
			svc.Poll()
			fmt.Printf("%-20s restarting...\n", svc.Def.ID)
			svc.Stop()
			time.Sleep(500 * time.Millisecond)
			if err := svc.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "%-20s start error: %v\n", svc.Def.ID, err)
			}
		}
		return nil
	},
}

func init() {
	restartCmd.Flags().StringVar(&restartTag, "tag", "", "filter services by tag")
}

// --- status ---

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show service status table",
	Long:  "Prints a formatted table of all services with their current status.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tPID\tPORT")
		fmt.Fprintln(w, "--\t----\t------\t---\t----")

		for _, svc := range services {
			svc.Poll()
			pid := "-"
			if svc.PID > 0 {
				pid = fmt.Sprintf("%d", svc.PID)
			}
			port := "-"
			if svc.Def.Port > 0 {
				port = fmt.Sprintf("%d", svc.Def.Port)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				svc.Def.ID, svc.Def.Name, svc.Status, pid, port)
		}
		w.Flush()
		return nil
	},
}

// --- logs ---

var logsFollow bool

var logsCmd = &cobra.Command{
	Use:   "logs <service>",
	Short: "Tail service logs",
	Long:  "Tails the log file for a service. Use -f to follow.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		var target *service.Service
		for _, svc := range services {
			if strings.EqualFold(svc.Def.ID, args[0]) {
				target = svc
				break
			}
		}
		if target == nil {
			return fmt.Errorf("service %q not found", args[0])
		}

		logPath := target.LogPath()
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			return fmt.Errorf("no log file found at %s", logPath)
		}

		if !logsFollow {
			data, err := os.ReadFile(logPath)
			if err != nil {
				return fmt.Errorf("reading log: %w", err)
			}
			fmt.Print(string(data))
			return nil
		}

		// Follow mode: read existing content then tail
		f, err := os.Open(logPath)
		if err != nil {
			return fmt.Errorf("opening log: %w", err)
		}
		defer f.Close()

		// Print existing content
		if _, err := io.Copy(os.Stdout, f); err != nil {
			return fmt.Errorf("reading log: %w", err)
		}

		// Follow new content
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

		for {
			select {
			case <-sig:
				return nil
			default:
				n, _ := io.Copy(os.Stdout, f)
				if n == 0 {
					time.Sleep(200 * time.Millisecond)
				}
			}
		}
	},
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "follow log output")
}

// --- build ---

var buildTag string

var buildCmd = &cobra.Command{
	Use:   "build [service...]",
	Short: "Run build commands",
	Long:  "Runs build commands for specified services (or all if none given). Use --tag to filter by tag.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		targets := filterServices(services, args, buildTag)
		if len(targets) == 0 {
			fmt.Println("No matching services found.")
			return nil
		}

		hasError := false
		for _, svc := range targets {
			if len(svc.Def.Build) == 0 {
				fmt.Printf("%-20s no build command configured, skipping\n", svc.Def.ID)
				continue
			}
			fmt.Printf("%-20s building...\n", svc.Def.ID)
			out, err := svc.BuildSync()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%-20s build failed:\n%s\n", svc.Def.ID, strings.TrimSpace(out))
				hasError = true
			} else {
				fmt.Printf("%-20s build ok\n", svc.Def.ID)
			}
		}
		if hasError {
			return fmt.Errorf("one or more builds failed")
		}
		return nil
	},
}

func init() {
	buildCmd.Flags().StringVar(&buildTag, "tag", "", "filter services by tag")
}

// --- rebuild ---

var rebuildTag string

var rebuildCmd = &cobra.Command{
	Use:   "rebuild [service...]",
	Short: "Build then restart services",
	Long:  "Builds specified services, then stops and restarts them. If build fails, the service is NOT restarted. Use --tag to filter by tag.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		targets := filterServices(services, args, rebuildTag)
		if len(targets) == 0 {
			fmt.Println("No matching services found.")
			return nil
		}

		hasError := false
		for _, svc := range targets {
			if len(svc.Def.Build) == 0 {
				fmt.Printf("%-20s no build command, restarting only...\n", svc.Def.ID)
			} else {
				fmt.Printf("%-20s building...\n", svc.Def.ID)
				out, err := svc.BuildSync()
				if err != nil {
					fmt.Fprintf(os.Stderr, "%-20s build failed, skipping restart:\n%s\n", svc.Def.ID, strings.TrimSpace(out))
					hasError = true
					continue
				}
				fmt.Printf("%-20s build ok\n", svc.Def.ID)
			}

			svc.Poll()
			svc.Stop()
			time.Sleep(500 * time.Millisecond)
			if err := svc.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "%-20s start error: %v\n", svc.Def.ID, err)
				hasError = true
			} else {
				fmt.Printf("%-20s restarted\n", svc.Def.ID)
			}
		}
		if hasError {
			return fmt.Errorf("one or more rebuild-restarts failed")
		}
		return nil
	},
}

func init() {
	rebuildCmd.Flags().StringVar(&rebuildTag, "tag", "", "filter services by tag")
}

// --- validate ---

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate configuration",
	Long:  "Loads and validates the config file, reporting any errors.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("config error: %w", err)
		}

		// Check for common issues
		errors := 0
		ids := make(map[string]bool)
		ports := make(map[int]string)

		for _, svc := range cfg.Services {
			if svc.ID == "" {
				fmt.Fprintf(os.Stderr, "  error: service missing ID (name: %s)\n", svc.Name)
				errors++
			}
			if ids[svc.ID] {
				fmt.Fprintf(os.Stderr, "  error: duplicate service ID %q\n", svc.ID)
				errors++
			}
			ids[svc.ID] = true

			if svc.Port > 0 {
				if other, ok := ports[svc.Port]; ok {
					fmt.Fprintf(os.Stderr, "  error: port %d used by both %q and %q\n", svc.Port, other, svc.ID)
					errors++
				}
				ports[svc.Port] = svc.ID
			}

			if len(svc.Command) == 0 {
				fmt.Fprintf(os.Stderr, "  warning: %s has no command\n", svc.ID)
			}
			if svc.Dir == "" {
				fmt.Fprintf(os.Stderr, "  warning: %s has no dir\n", svc.ID)
			}
		}

		if errors > 0 {
			return fmt.Errorf("config has %d error(s)", errors)
		}

		fmt.Printf("Config OK: %d services defined\n", len(cfg.Services))
		return nil
	},
}

// --- doctor ---

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check system health",
	Long:  "Checks ports, binaries, and working directories for all configured services.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		services := service.NewFromConfig(cfg)
		results := service.RunDoctor(services)

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "SERVICE\tCHECK\tSTATUS\tMESSAGE\n")
		for _, r := range results {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ServiceID, r.Check, r.Status, r.Message)
		}
		return w.Flush()
	},
}

// --- init ---

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create default config",
	Long:  "Creates the default configuration file if it does not exist.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.EnsureDefault(); err != nil {
			return fmt.Errorf("error creating config: %w", err)
		}
		fmt.Printf("Config written to %s\n", config.DefaultPath())
		return nil
	},
}

// --- daemon ---

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run daemon mode with health monitoring",
	Long:  "Starts Cerberus in daemon mode with health monitoring and auto-restart capabilities. Runs both MCP server and health monitor.",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		// Start health monitor
		monitorConfig := daemon.DefaultMonitorConfig()
		monitor := daemon.NewMonitor(services, monitorConfig)

		var wg sync.WaitGroup

		// Start monitor in background
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := monitor.Run(ctx); err != nil && err != context.Canceled {
				fmt.Fprintf(os.Stderr, "Monitor error: %v\n", err)
			}
		}()

		// Start MCP server in background
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv := mcp.NewServer("cerberus", "0.1.0")
			srv.RegisterTool(mcp.NewCerberusStatusTool(services, monitor))
			srv.RegisterTool(mcp.NewCerberusStartTool(services))
			srv.RegisterTool(mcp.NewCerberusStopTool(services))
			srv.RegisterTool(mcp.NewCerberusRestartTool(services))
			srv.RegisterTool(mcp.NewCerberusRebuildTool(services))
			srv.RegisterTool(mcp.NewCerberusLogsTool(services))
			srv.RegisterTool(mcp.NewCerberusBuildTool(services))
			srv.RegisterTool(mcp.NewCerberusHealthTool(services, monitor))

			if err := srv.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
			}
		}()

		fmt.Println("Cerberus daemon started - health monitoring and MCP server active")
		fmt.Println("Press Ctrl+C to stop...")

		// Wait for shutdown signal
		<-ctx.Done()
		fmt.Println("Shutting down...")

		// Stop monitor gracefully
		monitor.Stop()

		// Wait for all goroutines to finish
		wg.Wait()

		return nil
	},
}

// --- mcp ---

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP server",
	Long:  "Starts the MCP server for tool integration over stdio (JSON-RPC 2.0).",
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		srv := mcp.NewServer("cerberus", "0.1.0")
		srv.RegisterTool(mcp.NewCerberusStatusTool(services, nil))
		srv.RegisterTool(mcp.NewCerberusStartTool(services))
		srv.RegisterTool(mcp.NewCerberusStopTool(services))
		srv.RegisterTool(mcp.NewCerberusRestartTool(services))
		srv.RegisterTool(mcp.NewCerberusRebuildTool(services))
		srv.RegisterTool(mcp.NewCerberusLogsTool(services))
		srv.RegisterTool(mcp.NewCerberusBuildTool(services))
		srv.RegisterTool(mcp.NewCerberusHealthTool(services, nil))

		return srv.Run()
	},
}
