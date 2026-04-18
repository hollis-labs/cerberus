package main

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// daemonReplace keeps backward-compatible behavior for `cerberus daemon --replace`.
// The flag now routes through the same RestartWithVerify path as `daemon restart`.
var daemonReplace bool

// daemonForeground keeps the daemon in the foreground (no fork + detach).
// Used by the re-exec'd child via CERBERUS_DAEMON_CHILD=1 and by users who
// want to tail the daemon directly.
var daemonForeground bool

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run daemon mode with health monitoring",
	Long: `Starts Cerberus in daemon mode. Forks to background by default.
Use --foreground to stay in foreground.

Subcommands:
  cerberus daemon start    explicit start (same as bare 'daemon')
  cerberus daemon stop     stop the running daemon
  cerberus daemon restart  atomic stop-then-start with single-daemon invariant`,
	RunE: runDaemonStart,
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the cerberus daemon",
	Long:  "Starts the cerberus daemon. Refuses to start if another daemon already holds the lock.",
	RunE:  runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running cerberus daemon",
	Long:  "Sends SIGTERM to the running daemon and waits up to 5s for clean exit before escalating to SIGKILL.",
	RunE: func(cmd *cobra.Command, args []string) error {
		service.InitLifecycleLog()
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()

		pid, err := daemon.ReadDaemonPID()
		if err != nil || pid == 0 {
			fmt.Println("No cerberus daemon is running.")
			return nil
		}

		opts := daemon.DefaultStopOptions()
		if err := daemon.StopDaemon(ctx, pid, opts); err != nil {
			return fmt.Errorf("stop daemon: %w", err)
		}
		daemon.RemoveDaemonPID()
		fmt.Printf("Stopped cerberus daemon (PID %d)\n", pid)
		return nil
	},
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Atomically restart the cerberus daemon",
	Long: `Stops the running daemon (SIGTERM + wait, escalating to SIGKILL if needed),
sweeps any stray cerberus daemon processes, then starts a fresh daemon and
verifies it is healthy before returning. Enforces the single-daemon invariant.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		return runDaemonRestart(ctx)
	},
}

// runDaemonStart is the entrypoint for the bare `cerberus daemon` command and
// the explicit `cerberus daemon start` subcommand.
//
// When invoked with --replace it routes through RestartWithVerify, which is
// the CERB-5 fix: the old --replace path silently left the old daemon alive.
func runDaemonStart(cmd *cobra.Command, args []string) error {
	// If we're the re-exec'd child process, skip the fork dance and run the
	// real daemon body.
	if os.Getenv("CERBERUS_DAEMON_CHILD") == "1" {
		return runDaemonBody()
	}

	// Foreground mode: run the daemon body in-process without forking.
	if daemonForeground {
		return runDaemonBody()
	}

	service.InitLifecycleLog()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// --replace: perform an atomic restart.
	if daemonReplace {
		return runDaemonRestart(ctx)
	}

	// Plain start: refuse if another daemon is already running. The lockfile
	// acquired inside the child is the authoritative guard; this pre-check
	// gives the user a fast, descriptive error without incurring a fork.
	if existingPID, err := daemon.CheckDaemonRunning(); err != nil {
		return fmt.Errorf("checking daemon PID: %w", err)
	} else if existingPID > 0 {
		return fmt.Errorf("cerberus daemon already running (PID %d); use 'cerberus daemon restart' to replace", existingPID)
	}

	pid, err := spawnDaemonChild(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Cerberus daemon started (PID %d)\n", pid)
	return nil
}

// runDaemonRestart executes the atomic stop-then-start restart sequence.
func runDaemonRestart(ctx context.Context) error {
	service.InitLifecycleLog()
	logger := service.GetLogger()

	opts := daemon.DefaultRestartOptions()
	opts.Spawn = func(spawnCtx context.Context) (int, error) {
		return spawnDaemonChild(spawnCtx)
	}
	opts.Health = daemon.PIDFileHealth("", 100*time.Millisecond, daemon.PosixAliveChecker{})
	opts.HealthTimeout = 10 * time.Second

	if err := daemon.RestartWithVerify(ctx, opts); err != nil {
		logger.Error("daemon.restart.failed", "error", err.Error())
		return fmt.Errorf("daemon restart: %w", err)
	}

	pid, _ := daemon.ReadDaemonPID()
	fmt.Printf("Cerberus daemon restarted (PID %d)\n", pid)
	return nil
}

// spawnDaemonChild re-execs this binary with --foreground + CERBERUS_DAEMON_CHILD=1
// so the child runs runDaemonBody directly. Returns the child PID.
//
// Note: child writes the pidfile itself once it's up, so callers should use
// PIDFileHealth (or equivalent) to wait for readiness rather than trusting
// the return value as the final authoritative PID.
func spawnDaemonChild(ctx context.Context) (int, error) {
	spawn := daemon.ExecutableSpawner(
		[]string{"daemon", "--foreground"},
		[]string{"CERBERUS_DAEMON_CHILD=1"},
	)
	pid, err := spawn(ctx)
	if err != nil {
		return 0, fmt.Errorf("fork daemon child: %w", err)
	}
	return pid, nil
}

// runDaemonBody is the actual daemon process body: acquire the lock, write
// the pidfile, start the monitor + MCP server, wait for shutdown.
func runDaemonBody() error {
	service.InitLifecycleLog()
	logger := service.GetLogger()

	// Acquire the single-instance lock BEFORE doing anything else. This is
	// the authoritative guard against double-spawn (flock auto-releases on
	// process exit, so even SIGKILL can't leak it).
	lock, err := daemon.AcquireDaemonLock()
	if err != nil {
		var held *daemon.DaemonLockHeldError
		if errors.As(err, &held) {
			logger.Warn("daemon.lock.denied", "holder_pid", held.HolderPID)
			return err
		}
		return fmt.Errorf("acquire daemon lock: %w", err)
	}
	logger.Info("daemon.lock.acquired", "pid", os.Getpid())
	defer func() {
		_ = lock.Release()
	}()

	// Write our PID file atomically so MCP clients and status tools can find
	// us. This happens AFTER lock acquisition so observers never see a
	// pidfile that doesn't correspond to the live lock holder.
	if pidErr := daemon.WriteDaemonPID(); pidErr != nil {
		return fmt.Errorf("writing daemon PID file: %w", pidErr)
	}
	defer daemon.RemoveDaemonPID()

	a, err := app.New(cfgPath)
	if err != nil {
		return fmt.Errorf("init app: %w", err)
	}
	defer a.Close() //nolint:errcheck
	services := a.Services

	// Clean stale PID files from prior runs.
	if err := service.CleanStalePIDFiles(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to clean stale PID files: %v\n", err)
	}

	// Detect orphaned processes from prior Cerberus runs (log only, never kill).
	orphans := service.DetectOrphans(services)
	if len(orphans) > 0 {
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

	// Start health monitor.
	monitorConfig := daemon.DefaultMonitorConfig()
	monitor := daemon.NewMonitor(services, monitorConfig)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := monitor.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Monitor error: %v\n", err)
		}
	}()

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

	if daemonForeground && os.Getenv("CERBERUS_DAEMON_CHILD") == "" {
		fmt.Printf("Cerberus daemon running (PID %d) — press Ctrl+C to stop\n", os.Getpid())
	}

	<-ctx.Done()
	fmt.Println("Shutting down...")

	monitor.Stop()
	wg.Wait()

	return nil
}

func init() {
	daemonCmd.Flags().BoolVar(&daemonReplace, "replace", false, "restart: kill existing daemon before starting (routes through the atomic restart sequence)")
	daemonCmd.Flags().BoolVar(&daemonForeground, "foreground", false, "run in foreground instead of forking to background")

	daemonStartCmd.Flags().BoolVar(&daemonForeground, "foreground", false, "run in foreground instead of forking to background")

	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
}
