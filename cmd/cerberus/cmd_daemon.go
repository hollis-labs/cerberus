package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/chrispian/cerberus/internal/app"
	"github.com/chrispian/cerberus/internal/daemon"
	"github.com/chrispian/cerberus/internal/mcp"
	"github.com/chrispian/cerberus/internal/service"
	"github.com/spf13/cobra"
)

var daemonReplace bool

var daemonForeground bool

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run daemon mode with health monitoring",
	Long:  "Starts Cerberus in daemon mode. Forks to background by default. Use --foreground to stay in foreground.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// If not foreground and not already the forked child, fork and exit
		if !daemonForeground && os.Getenv("CERBERUS_DAEMON_CHILD") == "" {
			// Single-instance guard before forking
			existingPID, err := daemon.CheckDaemonRunning()
			if err != nil {
				return fmt.Errorf("checking daemon PID: %w", err)
			}
			if existingPID > 0 {
				if !daemonReplace {
					return fmt.Errorf("cerberus daemon already running (PID %d) — use 'cerberus daemon --replace' to take over", existingPID)
				}
				if killErr := daemon.KillDaemon(existingPID); killErr != nil {
					return fmt.Errorf("failed to kill existing daemon: %w", killErr)
				}
				fmt.Printf("Killed existing daemon (PID %d)\n", existingPID)
			}

			// Fork: re-exec ourselves with the child marker
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("finding executable: %w", err)
			}
			childArgs := []string{"daemon", "--foreground"}
			if daemonReplace {
				childArgs = append(childArgs, "--replace")
			}
			child := exec.Command(exe, childArgs...) //nolint:gosec // exe is from os.Executable(), not user input
			child.Env = append(os.Environ(), "CERBERUS_DAEMON_CHILD=1")
			child.Stdout = nil
			child.Stderr = nil
			child.Stdin = nil
			if err := child.Start(); err != nil {
				return fmt.Errorf("forking daemon: %w", err)
			}
			fmt.Printf("Cerberus daemon started (PID %d)\n", child.Process.Pid)
			// Detach — parent exits, child continues
			_ = child.Process.Release()
			return nil
		}

		// --- Below here is the actual daemon (child or --foreground) ---

		// Single-instance guard: check if another daemon is already running
		existingPID, err := daemon.CheckDaemonRunning()
		if err != nil {
			return fmt.Errorf("checking daemon PID: %w", err)
		}
		if existingPID > 0 {
			if !daemonReplace {
				return fmt.Errorf("Cerberus daemon already running (PID %d). Use 'cerberus daemon --replace' to take over.", existingPID) //nolint:revive,staticcheck
			}
			fmt.Printf("Replacing existing daemon (PID %d)...\n", existingPID)
			if err := daemon.KillDaemon(existingPID); err != nil { //nolint:govet
				return fmt.Errorf("failed to kill existing daemon: %w", err)
			}
			// Give the old daemon time to shut down
			time.Sleep(2 * time.Second)
		}

		// Write our PID file
		if err := daemon.WriteDaemonPID(); err != nil { //nolint:govet
			return fmt.Errorf("writing daemon PID file: %w", err)
		}
		defer daemon.RemoveDaemonPID()

		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck
		services := a.Services

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
			if err := monitor.Run(ctx); err != nil && err != context.Canceled { //nolint:errorlint
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
			srv.RegisterTool(mcp.NewCerberusProjectListTool(a.Config))
			srv.RegisterTool(mcp.NewCerberusResourceListTool(a.Config))
			srv.RegisterTool(mcp.NewCerberusPipelineListTool(a.Config))
			srv.RegisterTool(mcp.NewCerberusPipelineRunTool(a.Config, a.Services, a.Local))
			srv.RegisterTool(mcp.NewCerberusGithubStatusTool(a.Secrets))
			srv.RegisterTool(mcp.NewCerberusGithubReleasesTool(a.Secrets))
			srv.RegisterTool(mcp.NewCerberusGithubRunsTool(a.Secrets))

			// SSH tools
			srv.RegisterTool(mcp.NewCerberusSSHExecTool(a.Config, a.Secrets))
			srv.RegisterTool(mcp.NewCerberusSSHStatusTool(a.Config, a.Secrets))

			// Namecheap tools
			srv.RegisterTool(mcp.NewCerberusDomainListTool(a.Secrets))
			srv.RegisterTool(mcp.NewCerberusDomainStatusTool(a.Secrets))
			srv.RegisterTool(mcp.NewCerberusDNSListTool(a.Secrets))

			// Forge tools
			srv.RegisterTool(mcp.NewCerberusForgeServersTool(a.Secrets))
			srv.RegisterTool(mcp.NewCerberusForgeServerTool(a.Secrets))
			srv.RegisterTool(mcp.NewCerberusForgeSitesTool(a.Secrets))

			// Cloudflare tools
			srv.RegisterTool(mcp.NewCerberusCloudflareZonesTool(a.Secrets))
			srv.RegisterTool(mcp.NewCerberusCloudflareDNSListTool(a.Secrets))
			srv.RegisterTool(mcp.NewCerberusCloudflareDNSCreateTool(a.Secrets))

			// Docker tools
			srv.RegisterTool(mcp.NewCerberusDockerPSTool())
			srv.RegisterTool(mcp.NewCerberusDockerLogsTool())
			srv.RegisterTool(mcp.NewCerberusDockerUpTool())
			srv.RegisterTool(mcp.NewCerberusDockerDownTool())

			if err := srv.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
			}
		}()

		// Write PID file after all setup is done
		if err := daemon.WriteDaemonPID(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write PID file: %v\n", err)
		}
		defer daemon.RemoveDaemonPID()

		if daemonForeground && os.Getenv("CERBERUS_DAEMON_CHILD") == "" {
			// Only print interactive message if truly in foreground (not forked child)
			fmt.Printf("Cerberus daemon running (PID %d) — press Ctrl+C to stop\n", os.Getpid())
		}

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
	daemonCmd.Flags().BoolVar(&daemonForeground, "foreground", false, "run in foreground instead of forking to background")
}
