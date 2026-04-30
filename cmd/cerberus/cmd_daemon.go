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
	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/daemon"
	"github.com/chrispian/cerberus/internal/mcp"
	"github.com/chrispian/cerberus/internal/service"
	"github.com/spf13/cobra"
)

// daemonReplace keeps backward-compatible behavior for `cerberus daemon --replace`.
// The flag now routes through the same RestartWithVerify path as `daemon restart`.
var daemonReplace bool

// Restart-verification timings. Kept at package scope (rather than inlined at
// the call site) so the parallel with daemon.DefaultStopOptions /
// DefaultRestartOptions is explicit and drift-visible.
//
//   - daemonRestartPollInterval mirrors DefaultStopOptions().PollInterval.
//   - daemonRestartHealthTimeout intentionally differs from
//     DefaultRestartOptions().HealthTimeout (5s): the CLI path allows more
//     slack for the child to fork, detach, and write its pidfile.
const (
	daemonRestartPollInterval  = 100 * time.Millisecond
	daemonRestartHealthTimeout = 10 * time.Second
)

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

// daemonReloadCmd signals a running daemon to re-read its config.
//
// Under the hood it sends SIGHUP to the daemon process. The daemon routes
// SIGHUP, fsnotify events, and this subcommand through the same
// ServiceRegistry.Reload() call, so any of the three entry points produces
// identical behavior (add/remove/change diffing, last-good fallback on
// parse errors, structured config.reloaded log event).
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
	opts.Health = daemon.PIDFileHealth("", daemonRestartPollInterval, daemon.PosixAliveChecker{})
	opts.HealthTimeout = daemonRestartHealthTimeout

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

// runDaemonBody is the actual daemon process body: acquire the lock, install
// the SIGHUP handler, write the pidfile, start the resource monitor + MCP
// server, wait for shutdown.
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

	// Install SIGHUP handler BEFORE writing the PID file. The default
	// disposition for SIGHUP is to terminate the process, so a `cerberus
	// daemon reload` fired immediately after startup must not race the
	// goroutine that reads hupCh. Registering the channel now ensures any
	// delivered SIGHUP is queued rather than killing us.
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)

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
	// Signal handling:
	//   SIGINT / SIGTERM -> shutdown (ctx cancellation).
	//   SIGHUP            -> ignored; v2 config is re-read on demand.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start the active v2 resource monitor. Resource-native supervision
	// lives on the shared runtime side.
	resourceMonitor := cerbapi.NewResourceMonitor(a.Runtime, cerbapi.DefaultResourceMonitorConfig(), logger)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := resourceMonitor.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Resource monitor error: %v\n", err)
		}
	}()

	// SIGHUP is ignored in the v2-only runtime. Config is re-read on
	// demand by the shared runtime service and by the resource monitor.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				signal.Stop(hupCh)
				return
			case <-hupCh:
				logger.Info("daemon.sighup.ignored",
					"message", "config is re-read on demand in the v2 runtime")
			}
		}
	}()

	// Build the canonical InProcessClient. All MCP tool handlers
	// (daemon-embedded stdio MCP server AND external callers via the
	// unix socket) are backed by this single client so state lives in
	// one place.
	inProc := cerbapi.NewInProcessClient(
		cerbapi.WithConfigV2(a.Config),
		cerbapi.WithConfigPath(cfgPath),
		cerbapi.WithLocalConnector(a.Local),
		cerbapi.WithResourceRuntimeService(a.Runtime),
		cerbapi.WithInProcessLogger(logger),
	)

	// ---- Unix-socket RPC server (CERB-2). ----
	//
	// The standalone `cerberus mcp` subprocess dials this socket and
	// forwards every tool call to us, eliminating its own config
	// cache. Sits alongside (not instead of) the stdio MCP server
	// below so direct-MCP-over-stdio consumers still work.
	sockPath, sockErr := cerbapi.SocketPath()
	if sockErr != nil {
		logger.Warn("daemon.socket.path_resolve_failed", "error", sockErr.Error())
	} else {
		socketServer := cerbapi.NewSocketServer(inProc, sockPath, cerbapi.WithLogger(logger))
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := socketServer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("daemon.socket.exited", "error", err.Error())
			}
		}()
	}

	// ---- Stdio MCP server (existing surface). ----
	//
	// Preserved for consumers that invoke `cerberus daemon` directly
	// with stdio piping. Runs against the same InProcessClient so it
	// sees identical state to the socket-routed subprocess.
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv := mcp.NewServer("cerberus", "0.1.0")
		srv.RegisterTool(mcp.NewCerberusHealthTool(inProc))
		srv.RegisterTool(mcp.NewCerberusProjectListTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceListTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceStatusTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceInspectTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceDoctorTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceLogsTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceReloadTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceDeployTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceSyncTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceApplyTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceRemoveTool(inProc))
		srv.RegisterTool(mcp.NewCerberusPipelineListTool(inProc))
		srv.RegisterTool(mcp.NewCerberusPipelineRunTool(inProc))
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

	resourceMonitor.Stop()
	wg.Wait()

	return nil
}

func init() {
	daemonCmd.Flags().BoolVar(&daemonReplace, "replace", false, "restart: kill existing daemon before starting (routes through the atomic restart sequence)")
	daemonCmd.Flags().BoolVar(&daemonForeground, "foreground", false, "run in foreground instead of forking to background")

	// Mirror --replace + --foreground on `daemon start` so `cerberus daemon start --replace`
	// behaves identically to `cerberus daemon --replace` (both route through runDaemonStart,
	// which honors daemonReplace by calling runDaemonRestart).
	daemonStartCmd.Flags().BoolVar(&daemonReplace, "replace", false, "restart: kill existing daemon before starting (routes through the atomic restart sequence)")
	daemonStartCmd.Flags().BoolVar(&daemonForeground, "foreground", false, "run in foreground instead of forking to background")

	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
}
