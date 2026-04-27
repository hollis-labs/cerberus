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
	"github.com/chrispian/cerberus/internal/config"
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
  cerberus daemon restart  atomic stop-then-start with single-daemon invariant
  cerberus daemon reload   hot-reload config in the running daemon (SIGHUP)`,
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
var daemonReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Reload ~/.cerberus/config.yaml in the running daemon",
	Long: `Sends SIGHUP to the running Cerberus daemon to force a config reload.

Use this after editing ~/.cerberus/config.yaml when you want the changes
applied immediately rather than waiting for the file-watcher to notice.
This is the same mechanism as SIGHUP — added config entries are registered,
removed entries stop their processes, and changed entries are marked stale
(existing process keeps running on the old definition until the next
rebuild/restart).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		pid, err := daemon.CheckDaemonRunning()
		if err != nil {
			return fmt.Errorf("check daemon: %w", err)
		}
		if pid <= 0 {
			return fmt.Errorf("no running Cerberus daemon found (checked %s)", func() string {
				p, _ := daemon.DaemonPIDPath()
				return p
			}())
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("find daemon process %d: %w", pid, err)
		}
		if err := proc.Signal(syscall.SIGHUP); err != nil {
			return fmt.Errorf("send SIGHUP to %d: %w", pid, err)
		}
		fmt.Printf("Sent SIGHUP to daemon (PID %d) — check cerberus.log for config.reloaded event\n", pid)
		return nil
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
// the SIGHUP handler, write the pidfile, start the monitor + MCP server +
// config file-watcher, wait for shutdown.
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
	reg := a.ServiceRegistry

	// Clean stale PID files from prior runs.
	if err := service.CleanStalePIDFiles(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to clean stale PID files: %v\n", err)
	}

	// Detect orphaned processes from prior Cerberus runs (log only, never kill).
	orphans := service.DetectOrphans(reg.Current())
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

	// Signal handling:
	//   SIGINT / SIGTERM -> shutdown (ctx cancellation).
	//   SIGHUP            -> hot-reload config (routes through reg.Reload()).
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start health monitor.
	monitorConfig := daemon.DefaultMonitorConfig()
	monitor := daemon.NewMonitor(reg, monitorConfig)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := monitor.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Monitor error: %v\n", err)
		}
	}()

	// SIGHUP handler — delegates to the same Reload() used by the
	// file-watcher and the `cerberus daemon reload` subcommand so all three
	// entry points exercise identical code. (hupCh was already registered
	// above to avoid a startup race.)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				signal.Stop(hupCh)
				return
			case <-hupCh:
				logger.Info("daemon.sighup.received",
					"message", "reloading config from disk")
				if err := reg.Reload(); err != nil {
					// Registry already logged config.reload.failed;
					// also print to stderr for foreground operators.
					fmt.Fprintf(os.Stderr, "config reload failed: %v\n", err)
				}
			}
		}
	}()

	// File-watcher — best-effort observability. Reloads on any change to
	// the config file (coalesced via debounce). Init failure is logged and
	// ignored: we do not want a watcher hiccup to crash the daemon.
	watcher, werr := config.NewWatcher(cfgPath, func() {
		if err := reg.Reload(); err != nil {
			fmt.Fprintf(os.Stderr, "config reload (watcher) failed: %v\n", err)
		}
	}, logger)
	if werr != nil {
		logger.Warn("daemon.config_watcher.init_failed", "error", werr.Error())
	} else {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := watcher.Run(ctx); err != nil {
				logger.Warn("daemon.config_watcher.exited", "error", err.Error())
			}
		}()
	}

	// Build the canonical InProcessClient. All MCP tool handlers
	// (daemon-embedded stdio MCP server AND external callers via the
	// unix socket) are backed by this single client so state lives in
	// one place.
	inProc := cerbapi.NewInProcessClient(reg,
		cerbapi.WithMonitor(monitor),
		cerbapi.WithConfigV2(a.Config),
		cerbapi.WithConfigPath(cfgPath),
		cerbapi.WithLocalConnector(a.Local),
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
		srv.RegisterTool(mcp.NewCerberusStatusTool(inProc))
		srv.RegisterTool(mcp.NewCerberusStartTool(inProc))
		srv.RegisterTool(mcp.NewCerberusStopTool(inProc))
		srv.RegisterTool(mcp.NewCerberusRestartTool(inProc))
		srv.RegisterTool(mcp.NewCerberusRebuildTool(inProc))
		srv.RegisterTool(mcp.NewCerberusLogsTool(inProc))
		srv.RegisterTool(mcp.NewCerberusBuildTool(inProc))
		srv.RegisterTool(mcp.NewCerberusHealthTool(inProc))
		srv.RegisterTool(mcp.NewCerberusProjectListTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceListTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceStatusTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceInspectTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceDoctorTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceLogsTool(inProc))
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

	monitor.Stop()
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
	daemonCmd.AddCommand(daemonReloadCmd)
}
