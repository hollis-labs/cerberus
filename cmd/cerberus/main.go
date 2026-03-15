package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"text/template"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/daemon"
	"github.com/chrispian/cerberus/internal/mcp"
	"github.com/chrispian/cerberus/internal/pausectl"
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
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(uninstallCmd)
	rootCmd.AddCommand(pauseCmd)
	rootCmd.AddCommand(resumeCmd)
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

// --- up ---

var upTag string

var upCmd = &cobra.Command{
	Use:   "up [service...]",
	Short: "Start services headlessly",
	Long:  "Starts specified services (or all if none given) in dependency order. Use --tag to filter by tag.",
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

		// Use Manager for dependency-ordered startup.
		mgr, err := service.NewServiceManager(services)
		if err != nil {
			// Fallback: start without ordering if DAG fails.
			fmt.Fprintf(os.Stderr, "Warning: dependency ordering unavailable: %v\n", err)
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
		}

		// If starting all services, use StartAll for full dependency ordering.
		if len(args) == 0 && upTag == "" {
			errs := mgr.StartAll()
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "  error: %v\n", e)
			}
			if len(errs) == 0 {
				fmt.Printf("All %d services starting in dependency order.\n", len(targets))
			}
			return nil
		}

		// Starting specific services: start each with auto-deps.
		for _, svc := range targets {
			svc.Poll()
			if svc.Status == service.StatusRunning || svc.Status == service.StatusHealthy {
				fmt.Printf("%-20s already running (pid %d)\n", svc.Def.ID, svc.PID)
				continue
			}
			errs := mgr.StartService(svc.Def.ID, true)
			if len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintf(os.Stderr, "%-20s error: %v\n", svc.Def.ID, e)
				}
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
	Long:  "Stops specified services (or all if none given) in reverse dependency order. Use --tag to filter by tag.",
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

		// Use Manager for reverse-dependency ordered shutdown.
		mgr, err := service.NewServiceManager(services)
		if err != nil {
			// Fallback: stop without ordering if DAG fails.
			fmt.Fprintf(os.Stderr, "Warning: dependency ordering unavailable: %v\n", err)
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
		}

		// Explicit bulk stop: user issued `cerberus down` (all services).
		explicitBulk := len(args) == 0 && downTag == ""

		if explicitBulk {
			fmt.Printf("Stopping all %d services in reverse dependency order...\n", len(targets))
			errs := mgr.StopAll()
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "  error: %v\n", e)
			}
			return nil
		}

		// Stopping a subset: use StopSubset with bulk guard.
		errs := mgr.StopSubset(targets, false)
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  error: %v\n", e)
		}
		if len(errs) == 0 {
			for _, svc := range targets {
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

		var target *service.ManagedService
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

var daemonReplace bool

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run daemon mode with health monitoring",
	Long:  "Starts Cerberus in daemon mode with health monitoring and auto-restart capabilities. Runs both MCP server and health monitor.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Single-instance guard: check if another daemon is already running
		existingPID, err := daemon.CheckDaemonRunning()
		if err != nil {
			return fmt.Errorf("checking daemon PID: %w", err)
		}
		if existingPID > 0 {
			if !daemonReplace {
				return fmt.Errorf("Cerberus daemon already running (PID %d). Use 'cerberus daemon --replace' to take over.", existingPID)
			}
			fmt.Printf("Replacing existing daemon (PID %d)...\n", existingPID)
			if err := daemon.KillDaemon(existingPID); err != nil {
				return fmt.Errorf("failed to kill existing daemon: %w", err)
			}
			// Give the old daemon time to shut down
			time.Sleep(2 * time.Second)
		}

		// Write our PID file
		if err := daemon.WriteDaemonPID(); err != nil {
			return fmt.Errorf("writing daemon PID file: %w", err)
		}
		defer daemon.RemoveDaemonPID()

		services, err := loadServices()
		if err != nil {
			return err
		}

		// Clean stale PID files from prior runs
		if err := service.CleanStalePIDFiles(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clean stale PID files: %v\n", err)
		}

		// Detect orphaned processes from prior Cerberus runs (log only, never kill)
		orphans := service.DetectOrphans(services)
		if len(orphans) > 0 {
			logger := service.GetLogger()
			logger.Warn("daemon.orphan_detection",
				"count", len(orphans),
				"message", fmt.Sprintf("Found %d potential orphan process(es) from prior runs", len(orphans)),
			)
			for _, o := range orphans {
				logger.Warn("daemon.orphan_detected",
					"service", o.ServiceID,
					"pid", o.PID,
					"port", o.Port,
					"process", o.ProcessName,
				)
			}
			fmt.Printf("Warning: detected %d orphaned process(es) from prior runs. See cerberus.log for details.\n", len(orphans))
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

		fmt.Printf("Cerberus daemon started (PID %d) - health monitoring and MCP server active\n", os.Getpid())
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

func init() {
	daemonCmd.Flags().BoolVar(&daemonReplace, "replace", false, "kill existing daemon before starting")
}

// --- pause ---

var pauseCmd = &cobra.Command{
	Use:   "pause [service-id]",
	Short: "Pause auto-restart",
	Long:  "Pauses auto-restart for all services, or a specific service if an ID is given. While paused, services that crash will not be automatically restarted.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			serviceID := args[0]
			if err := pausectl.PauseService(serviceID); err != nil {
				return fmt.Errorf("pausing service %s: %w", serviceID, err)
			}
			fmt.Printf("Auto-restart paused for %s. Run 'cerberus resume %s' to re-enable.\n", serviceID, serviceID)
			return nil
		}
		if err := pausectl.PauseAll(); err != nil {
			return fmt.Errorf("pausing auto-restart: %w", err)
		}
		fmt.Println("Auto-restart paused. Run 'cerberus resume' to re-enable.")
		return nil
	},
}

// --- resume ---

var resumeCmd = &cobra.Command{
	Use:   "resume [service-id]",
	Short: "Resume auto-restart",
	Long:  "Resumes auto-restart for all services, or a specific service if an ID is given.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			serviceID := args[0]
			if err := pausectl.ResumeService(serviceID); err != nil {
				return fmt.Errorf("resuming service %s: %w", serviceID, err)
			}
			fmt.Printf("Auto-restart resumed for %s.\n", serviceID)
			return nil
		}
		if err := pausectl.ResumeAll(); err != nil {
			return fmt.Errorf("resuming auto-restart: %w", err)
		}
		fmt.Println("Auto-restart resumed.")
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

// --- install / uninstall ---

const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.tiamat.cerberus</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.BinaryPath}}</string>
        <string>daemon</string>
    </array>
    <key>WorkingDirectory</key>
    <string>{{.WorkingDir}}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{.HomeDir}}/.cerberus/logs/launchd-stdout.log</string>
    <key>StandardErrorPath</key>
    <string>{{.HomeDir}}/.cerberus/logs/launchd-stderr.log</string>
</dict>
</plist>
`

const launchdPlistName = "com.tiamat.cerberus.plist"

type launchdData struct {
	BinaryPath string
	WorkingDir string
	HomeDir    string
}

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install cerberus as a system service",
	Long:  "Installs a macOS launch agent so the cerberus daemon starts automatically on login and restarts if it exits.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("install is currently supported on macOS only")
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("could not determine home directory: %w", err)
		}

		binPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("could not determine binary path: %w", err)
		}
		binPath, err = filepath.EvalSymlinks(binPath)
		if err != nil {
			return fmt.Errorf("could not resolve binary path: %w", err)
		}

		workDir := filepath.Dir(binPath)

		// Ensure logs directory exists
		logsDir := filepath.Join(home, ".cerberus", "logs")
		if err := os.MkdirAll(logsDir, 0755); err != nil {
			return fmt.Errorf("creating logs directory: %w", err)
		}

		// Render the plist template
		tmpl, err := template.New("plist").Parse(launchdPlistTemplate)
		if err != nil {
			return fmt.Errorf("parsing plist template: %w", err)
		}

		plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdPlistName)

		// Unload existing agent if present
		if _, err := os.Stat(plistPath); err == nil {
			exec.Command("launchctl", "unload", plistPath).Run()
		}

		f, err := os.Create(plistPath)
		if err != nil {
			return fmt.Errorf("creating plist: %w", err)
		}

		data := launchdData{
			BinaryPath: binPath,
			WorkingDir: workDir,
			HomeDir:    home,
		}
		if err := tmpl.Execute(f, data); err != nil {
			f.Close()
			return fmt.Errorf("writing plist: %w", err)
		}
		f.Close()

		// Load the agent
		if out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl load failed: %s: %w", strings.TrimSpace(string(out)), err)
		}

		fmt.Printf("Installed launch agent: %s\n", plistPath)
		fmt.Printf("Binary: %s\n", binPath)
		fmt.Println("Cerberus daemon will start automatically on login.")
		return nil
	},
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove cerberus system service",
	Long:  "Unloads and removes the macOS launch agent for the cerberus daemon.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("uninstall is currently supported on macOS only")
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("could not determine home directory: %w", err)
		}

		plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdPlistName)

		if _, err := os.Stat(plistPath); os.IsNotExist(err) {
			fmt.Println("Launch agent not installed, nothing to do.")
			return nil
		}

		// Unload the agent
		exec.Command("launchctl", "unload", plistPath).Run()

		// Remove the plist
		if err := os.Remove(plistPath); err != nil {
			return fmt.Errorf("removing plist: %w", err)
		}

		fmt.Printf("Removed launch agent: %s\n", plistPath)
		fmt.Println("Cerberus daemon will no longer start automatically.")
		return nil
	},
}
