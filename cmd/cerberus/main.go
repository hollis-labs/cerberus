package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/mcp"
	"github.com/chrispian/cerberus/internal/service"
	"github.com/chrispian/cerberus/internal/tui"
	"github.com/spf13/cobra"
)

var cfgPath string

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// rootCmd launches the TUI when no subcommand is given.
var rootCmd = &cobra.Command{
	Use:   "cerberus",
	Short: "Tiamat service manager",
	Long:  "Cerberus is a TUI service manager for the Tiamat ecosystem.",
	RunE:  runTUI,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", config.DefaultPath(), "path to config file")

	rootCmd.AddCommand(upCmd)
	rootCmd.AddCommand(downCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(buildCmd)
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(mcpCmd)
}

// runTUI launches the interactive Bubble Tea TUI (default behavior).
func runTUI(cmd *cobra.Command, args []string) error {
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
	m := tui.NewModel(services)

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	return nil
}

// loadServices loads config and creates service objects.
func loadServices() ([]*service.Service, error) {
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
	Long:  "Checks system health and reports any issues.",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Not implemented yet")
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
		srv.RegisterTool(mcp.NewCerberusStatusTool(services))
		srv.RegisterTool(mcp.NewCerberusStartTool(services))
		srv.RegisterTool(mcp.NewCerberusStopTool(services))
		srv.RegisterTool(mcp.NewCerberusRestartTool(services))
		srv.RegisterTool(mcp.NewCerberusLogsTool(services))
		srv.RegisterTool(mcp.NewCerberusBuildTool(services))
		srv.RegisterTool(mcp.NewCerberusHealthTool(services))

		return srv.Run()
	},
}
