package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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

// daemonOverrideLaunchd lets an operator bypass the "refuse manual start
// when launchd-managed" guard for debugging. Without it, `cerberus daemon`
// (or `... --foreground`) on a launchd-managed install refuses to start
// rather than squatting the daemon lock the launchd-supervised service
// would otherwise hold (CW-20260519-0054).
var daemonOverrideLaunchd bool

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
	Long: `Sends SIGTERM to the running daemon and waits up to 5s for clean exit before
escalating to SIGKILL. When the daemon is launchd-managed, signals via
'launchctl kill SIGTERM' so the supervisor sees the shutdown rather than
treating it as a crash.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		service.InitLifecycleLog()
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()

		if daemon.LaunchdManagedDaemonExists() && !daemon.LaunchdSpawnedSelf() && !daemonOverrideLaunchd {
			out, err := daemon.LaunchctlKill(ctx)
			if err != nil {
				return fmt.Errorf("launchctl kill SIGTERM %s: %w (output: %s)",
					daemon.LaunchdServiceTarget(), err, string(out))
			}
			fmt.Printf("Sent SIGTERM via launchd (%s). Note: KeepAlive=true will restart the service.\n",
				daemon.LaunchdServiceTarget())
			return nil
		}

		pid, err := daemonPIDForControl(ctx)
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

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon process and socket status",
	RunE: func(cmd *cobra.Command, args []string) error {
		status, err := currentDaemonStatus(cmd.Context())
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), status)
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
	// real daemon body. Lock acquisition inside runDaemonBody is the
	// authoritative single-instance guard.
	if os.Getenv("CERBERUS_DAEMON_CHILD") == "1" {
		return runDaemonBody()
	}

	// Foreground mode: run the daemon body in-process without forking.
	// This is the path launchd uses (`cerberus daemon --foreground`). When
	// launchd is the intended supervisor but XPC_SERVICE_NAME is absent
	// we're a manual operator invocation that would squat the lock — refuse
	// unless explicitly overridden (CW-20260519-0054).
	if daemonForeground {
		if err := refuseIfLaunchdManagedAndManual(); err != nil {
			return err
		}
		return runDaemonBody()
	}

	service.InitLifecycleLog()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// --replace: perform an atomic restart.
	if daemonReplace {
		return runDaemonRestart(ctx)
	}

	// Route CLI start through launchctl when launchd is the intended
	// supervisor so we don't fork a parallel daemon that would later lose
	// the lock race or — worse — win it and crash-loop the launchd job.
	if daemon.LaunchdManagedDaemonExists() && !daemon.LaunchdSpawnedSelf() && !daemonOverrideLaunchd {
		return launchctlStartOrRestart(ctx, false)
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

// refuseIfLaunchdManagedAndManual returns a structured error when the
// canonical launchd plist exists and the current process was not spawned
// by launchd. The error message names the correct remediation paths so
// operators don't reach for `kill -9` on the running launchd-supervised
// daemon.
func refuseIfLaunchdManagedAndManual() error {
	if daemonOverrideLaunchd {
		return nil
	}
	if !daemon.LaunchdManagedDaemonExists() || daemon.LaunchdSpawnedSelf() {
		return nil
	}
	plistPath, _ := daemon.LaunchdManagedDaemonPlistPath()
	return fmt.Errorf(`refusing to start a manual cerberus daemon: launchd plist exists at %s.
  - To kick the launchd-managed daemon: cerberus resource reload %s
  - To restart from scratch:           cerberus resource apply  %s
  - To debug a parallel daemon (rare): cerberus daemon --foreground --override-launchd`,
		plistPath, daemon.CanonicalDaemonResourceID, daemon.CanonicalDaemonResourceID)
}

// launchctlStartOrRestart shells out to `launchctl kickstart` (or
// `kickstart -k` when restart=true) for the canonical Cerberus daemon
// service. Used by `cerberus daemon start` / `restart` when launchd is the
// intended supervisor — eliminates the manual-daemon-squatter failure mode
// at its source.
func launchctlStartOrRestart(ctx context.Context, restart bool) error {
	action := "start"
	if restart {
		action = "restart"
	}
	out, err := daemon.LaunchctlKickstart(ctx, restart)
	if err != nil {
		return fmt.Errorf("launchctl kickstart %s: %w (output: %s)",
			daemon.LaunchdServiceTarget(), err, string(out))
	}
	fmt.Printf("Cerberus daemon %sed via launchd (%s)\n", action, daemon.LaunchdServiceTarget())
	return nil
}

// runDaemonRestart executes the atomic stop-then-start restart sequence.
// When launchd is the intended supervisor, restart routes through
// `launchctl kickstart -k` so the launchd job — not a manual fork — owns
// the next daemon instance (CW-20260519-0054).
func runDaemonRestart(ctx context.Context) error {
	service.InitLifecycleLog()
	logger := service.GetLogger()

	if daemon.LaunchdManagedDaemonExists() && !daemon.LaunchdSpawnedSelf() && !daemonOverrideLaunchd {
		return launchctlStartOrRestart(ctx, true)
	}

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

func daemonPIDForControl(ctx context.Context) (int, error) {
	pid, err := daemon.ReadDaemonPID()
	if err == nil && pid > 0 {
		return pid, nil
	}

	holderPID, _, lockErr := daemon.DaemonLockHolder()
	if lockErr != nil || holderPID <= 0 {
		if err != nil {
			return 0, err
		}
		return 0, lockErr
	}

	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ok, probeErr := (daemon.PSIdentifier{}).IsCerberusDaemon(probeCtx, holderPID)
	if probeErr != nil || !ok {
		if err != nil {
			return 0, err
		}
		if probeErr != nil {
			return 0, probeErr
		}
		return 0, nil
	}
	return holderPID, nil
}

type daemonStatusView struct {
	Running        bool   `json:"running"`
	PID            int    `json:"pid,omitempty"`
	PIDSource      string `json:"pid_source,omitempty"`
	Origin         string `json:"origin,omitempty"`
	LaunchdManaged bool   `json:"launchd_managed"`
	OriginMismatch bool   `json:"origin_mismatch,omitempty"`
	SocketPath     string `json:"socket_path,omitempty"`
	SocketPresent  bool   `json:"socket_present"`
	SocketReady    bool   `json:"socket_ready"`
	LockHolderPID  int    `json:"lock_holder_pid,omitempty"`
	Error          string `json:"error,omitempty"`
}

func currentDaemonStatus(ctx context.Context) (daemonStatusView, error) {
	out := daemonStatusView{}
	out.LaunchdManaged = daemon.LaunchdManagedDaemonExists()

	pid, err := daemon.ReadDaemonPID()
	if err == nil && pid > 0 {
		out.PID = pid
		out.PIDSource = "pidfile"
		out.Running = true
	} else {
		holderPID, _, lockErr := daemon.DaemonLockHolder()
		if lockErr == nil && holderPID > 0 {
			out.LockHolderPID = holderPID
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			ok, probeErr := (daemon.PSIdentifier{}).IsCerberusDaemon(probeCtx, holderPID)
			cancel()
			if probeErr == nil && ok {
				out.PID = holderPID
				out.PIDSource = "lock"
				out.Running = true
			}
		}
	}

	if info, lockErr := daemon.ReadDaemonLockInfo(); lockErr == nil {
		out.Origin = info.Origin
		if out.LaunchdManaged && out.Running && info.Origin == "manual" {
			out.OriginMismatch = true
		}
	}

	socketPath, sockErr := cerbapi.SocketPath()
	if sockErr != nil {
		out.Error = sockErr.Error()
		return out, nil
	}
	out.SocketPath = socketPath
	if _, err := os.Stat(socketPath); err == nil {
		out.SocketPresent = true
	}

	client := cerbapi.NewSocketClient(socketPath)
	pingCtx, cancel := context.WithTimeout(ctx, cerbapi.DialTimeout)
	defer cancel()
	if err := client.Ping(pingCtx); err == nil {
		out.SocketReady = true
		if !out.Running {
			out.Running = true
		}
	} else if out.Error == "" {
		out.Error = err.Error()
	}

	if !out.SocketPresent && out.SocketPath != "" {
		out.Error = fmt.Sprintf("socket missing at %s", filepath.Clean(out.SocketPath))
	}
	return out, nil
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

	a, err := app.NewWithOptions(appOptions())
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
	executable, executableErr := os.Executable()
	if executableErr != nil {
		return fmt.Errorf("identify serving daemon: %w", executableErr)
	}
	a.Runtime.ProtectServingDaemon(executable, os.Getenv("XPC_SERVICE_NAME"))
	resourceMonitor := cerbapi.NewResourceMonitor(a.Runtime, cerbapi.DefaultResourceMonitorConfig(), logger)

	// Start the background artifact-drift scan and wire it into the
	// runtime so the high-fanout list path surfaces repo-drift staleness
	// without a live git probe per poll.
	driftCache := cerbapi.NewDriftCache(a.Runtime, logger)
	a.Runtime.AttachDriftCache(driftCache)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := resourceMonitor.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Resource monitor error: %v\n", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := driftCache.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Drift cache error: %v\n", err)
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
	statePath, stateErr := cerbapi.PluginConnectorStatePath()
	if stateErr != nil {
		return fmt.Errorf("resolve plugin connector state path: %w", stateErr)
	}
	managedPlugins, managedErr := cerbapi.NewManagedPluginConnectorService(version, os.Stderr, statePath)
	if managedErr != nil {
		return fmt.Errorf("initialize managed plugin connectors: %w", managedErr)
	}
	external := cerbapi.NewExternalConnectorService(a.Registry, managedPlugins)
	inProc := cerbapi.NewInProcessClient(
		cerbapi.WithConfigV2(a.Config),
		cerbapi.WithConfigPath(cfgPath),
		cerbapi.WithLocalConnector(a.Local),
		cerbapi.WithResourceRuntimeService(a.Runtime),
		cerbapi.WithExternalConnectorService(external),
		cerbapi.WithPluginConnectorService(cerbapi.NewPluginConnectorService(version, os.Stderr)),
		cerbapi.WithManagedPluginConnectorService(managedPlugins),
		cerbapi.WithInProcessLogger(logger),
	)

	// Start the overview snapshot recorder. Samples control-plane
	// counters (resources, projects, running/attention/stopped, registry
	// health, etc.) on a 1-minute tick and persists them to
	// ~/.cerberus/state/overview_snapshots.json. The web overview API
	// reads that file and buckets the samples into a 24h trend so the
	// dashboard's SignalBars + MiniTrend widgets render real history.
	snapshotPath, snapshotErr := cerbapi.SnapshotStatePath()
	if snapshotErr != nil {
		logger.Warn("daemon.snapshot.path_resolve_failed", "error", snapshotErr.Error())
	} else {
		snapshotCfg := cerbapi.DefaultSnapshotRecorderConfig()
		snapshotCfg.Path = snapshotPath
		recorder := cerbapi.NewSnapshotRecorder(
			snapshotCfg,
			cerbapi.NewClientSnapshotFunc(inProc, cfgPath, logger),
			logger,
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := recorder.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("daemon.snapshot.exited", "error", err.Error())
			}
		}()
	}

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
		srv.RegisterTool(mcp.NewCerberusResourceStopTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceDeployTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceEnsureFreshTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceSyncTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceApplyTool(inProc))
		srv.RegisterTool(mcp.NewCerberusResourceRemoveTool(inProc))
		srv.RegisterTool(mcp.NewCerberusPipelineListTool(inProc))
		srv.RegisterTool(mcp.NewCerberusPipelineRunTool(inProc))
		srv.RegisterTool(mcp.NewCerberusConnectorListTool(inProc))
		srv.RegisterTool(mcp.NewCerberusConnectorDescribeTool(inProc))
		srv.RegisterTool(mcp.NewCerberusGithubStatusTool(inProc))
		srv.RegisterTool(mcp.NewCerberusGithubReleasesTool(inProc))
		srv.RegisterTool(mcp.NewCerberusGithubRunsTool(inProc))

		// SSH tools
		srv.RegisterTool(mcp.NewCerberusSSHExecTool(a.Config, inProc))
		srv.RegisterTool(mcp.NewCerberusSSHStatusTool(a.Config, inProc))

		// Namecheap tools
		srv.RegisterTool(mcp.NewCerberusDomainListTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDomainStatusTool(inProc))
		srv.RegisterTool(mcp.NewCerberusNameserversSetTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDNSListTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDNSCreateTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDNSDeleteTool(inProc))

		// Forge tools
		srv.RegisterTool(mcp.NewCerberusForgeServersTool(inProc))
		srv.RegisterTool(mcp.NewCerberusForgeServerTool(inProc))
		srv.RegisterTool(mcp.NewCerberusForgeSitesTool(inProc))
		srv.RegisterTool(mcp.NewCerberusForgeDeployTool(inProc))
		srv.RegisterTool(mcp.NewCerberusForgeExecTool(inProc))

		// Cloudflare tools
		srv.RegisterTool(mcp.NewCerberusCloudflareZonesTool(inProc))
		srv.RegisterTool(mcp.NewCerberusCloudflareZoneCreateTool(inProc))
		srv.RegisterTool(mcp.NewCerberusCloudflareDNSListTool(inProc))
		srv.RegisterTool(mcp.NewCerberusCloudflareDNSCreateTool(inProc))
		srv.RegisterTool(mcp.NewCerberusCloudflareDNSDeleteTool(inProc))

		// Docker tools
		srv.RegisterTool(mcp.NewCerberusDockerPSTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDockerLogsTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDockerUpTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDockerDownTool(inProc))

		srv.RegisterTool(mcp.NewCerberusDropletListTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDropletGetTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDropletCreateTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDropletStartTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDropletStopTool(inProc))
		srv.RegisterTool(mcp.NewCerberusDropletDestroyTool(inProc))

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
	daemonCmd.Flags().BoolVar(&daemonOverrideLaunchd, "override-launchd", false, "bypass the launchd-managed guard and route the manual path (for debugging only)")

	// Mirror --replace + --foreground on `daemon start` so `cerberus daemon start --replace`
	// behaves identically to `cerberus daemon --replace` (both route through runDaemonStart,
	// which honors daemonReplace by calling runDaemonRestart).
	daemonStartCmd.Flags().BoolVar(&daemonReplace, "replace", false, "restart: kill existing daemon before starting (routes through the atomic restart sequence)")
	daemonStartCmd.Flags().BoolVar(&daemonForeground, "foreground", false, "run in foreground instead of forking to background")
	daemonStartCmd.Flags().BoolVar(&daemonOverrideLaunchd, "override-launchd", false, "bypass the launchd-managed guard (for debugging only)")
	daemonStopCmd.Flags().BoolVar(&daemonOverrideLaunchd, "override-launchd", false, "bypass the launchd-managed guard (for debugging only)")
	daemonRestartCmd.Flags().BoolVar(&daemonOverrideLaunchd, "override-launchd", false, "bypass the launchd-managed guard (for debugging only)")

	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
}
