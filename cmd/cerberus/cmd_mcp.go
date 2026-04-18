package main

import (
	"context"
	"fmt"
	"os"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/mcp"
	"github.com/chrispian/cerberus/internal/secrets"
	"github.com/chrispian/cerberus/internal/selfexec"
	"github.com/chrispian/cerberus/internal/service"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP server (thin RPC client to the cerberus daemon)",
	Long: `Starts the MCP server for tool integration over stdio (JSON-RPC 2.0).

The standalone subprocess holds NO config state of its own: it dials
the running 'cerberus daemon' over ~/.cerberus/cerberus.sock and
forwards every tool call to the daemon, which owns the single source
of truth (live ServiceRegistry, hot-reloaded on every op).

If no daemon is running, tool calls return a structured error prompting
the operator to start one with 'cerberus daemon'.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		service.InitLifecycleLog()
		logger := service.GetLogger()

		sockPath, err := cerbapi.SocketPath()
		if err != nil {
			return fmt.Errorf("resolve socket path: %w", err)
		}

		socketClient := cerbapi.NewSocketClient(sockPath, cerbapi.WithClientLogger(logger))

		// Best-effort connectivity check. We log rather than hard-fail
		// because the daemon might start after the MCP subprocess (e.g.
		// Claude Code spawns MCP first, operator then runs
		// 'cerberus daemon'). Tool calls will surface
		// DaemonUnreachableError until the daemon is up.
		pingCtx, cancel := context.WithTimeout(cmd.Context(), cerbapi.DialTimeout)
		if pingErr := socketClient.Ping(pingCtx); pingErr != nil {
			logger.Warn("client.mcp.daemon_unreachable_on_boot",
				"path", sockPath,
				"error", pingErr.Error(),
				"message", "MCP subprocess will keep running; tool calls will fail until cerberus daemon is started")
			// Hint to stderr too so operators tailing stdio see it.
			fmt.Fprintf(os.Stderr, "WARN: cerberus daemon not reachable at %s; start it with 'cerberus daemon' for tool calls to succeed.\n", sockPath)
		}
		cancel()

		// Secrets provider is still needed for connector-based tools
		// (SSH, Forge, Cloudflare, etc.) that do NOT route through the
		// daemon. These are read-only external-service adapters with
		// no config-staleness risk — the daemon doesn't need to own
		// them to fix CERB-2. Keep them local for now.
		sec := secrets.NewKeychainProvider()

		srv := mcp.NewServer("cerberus", "0.1.0")

		// Lifecycle + resource tools route through the socket client.
		// Every tool call forwards to the daemon, which owns the live
		// registry + config. No per-subprocess cache → no staleness.
		srv.RegisterTool(mcp.NewCerberusStatusTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusStartTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusStopTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusRestartTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusRebuildTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusLogsTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusBuildTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusHealthTool(socketClient))

		// Project / resource / pipeline tools go through the socket too
		// so their data is consistent with what the daemon sees.
		srv.RegisterTool(mcp.NewCerberusProjectListTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceListTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusPipelineListTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusPipelineRunTool(socketClient))

		// Connector-based tools (external integrations). Out of scope
		// for CERB-2 — they read secrets from keychain and hit remote
		// APIs; they don't touch service config.
		srv.RegisterTool(mcp.NewCerberusGithubStatusTool(sec))
		srv.RegisterTool(mcp.NewCerberusGithubReleasesTool(sec))
		srv.RegisterTool(mcp.NewCerberusGithubRunsTool(sec))

		// SSH tools (need Config for host resolution). We still load
		// config here because SSH targets live in v2 config, not in
		// the service registry — the daemon doesn't know about them.
		// This is a narrow, read-only surface; it does NOT manage
		// service lifecycle state and therefore is not part of the
		// CERB-2 staleness problem.
		cfg, cfgErr := loadUnifiedForTools(cfgPath)
		if cfgErr != nil {
			logger.Warn("client.mcp.config_load_failed",
				"path", cfgPath,
				"error", cfgErr.Error(),
				"message", "SSH tools will be unavailable; daemon-routed tools still work")
		} else {
			srv.RegisterTool(mcp.NewCerberusSSHExecTool(cfg, sec))
			srv.RegisterTool(mcp.NewCerberusSSHStatusTool(cfg, sec))
		}

		// Namecheap, Forge, Cloudflare, Docker — external adapters.
		srv.RegisterTool(mcp.NewCerberusDomainListTool(sec))
		srv.RegisterTool(mcp.NewCerberusDomainStatusTool(sec))
		srv.RegisterTool(mcp.NewCerberusDNSListTool(sec))

		srv.RegisterTool(mcp.NewCerberusForgeServersTool(sec))
		srv.RegisterTool(mcp.NewCerberusForgeServerTool(sec))
		srv.RegisterTool(mcp.NewCerberusForgeSitesTool(sec))

		srv.RegisterTool(mcp.NewCerberusCloudflareZonesTool(sec))
		srv.RegisterTool(mcp.NewCerberusCloudflareDNSListTool(sec))
		srv.RegisterTool(mcp.NewCerberusCloudflareDNSCreateTool(sec))

		srv.RegisterTool(mcp.NewCerberusDockerPSTool())
		srv.RegisterTool(mcp.NewCerberusDockerLogsTool())
		srv.RegisterTool(mcp.NewCerberusDockerUpTool())
		srv.RegisterTool(mcp.NewCerberusDockerDownTool())

		// CERB-4: self-heal on binary replacement. If `cerberus rebuild`,
		// `go install`, a package manager, or anything else swaps this
		// binary on disk while we're running, exit cleanly so the parent
		// MCP host (Claude Code, Nanite, etc.) respawns us against the
		// new binary on the next tool call. Default 30s polling cadence.
		// Thread the lifecycle logger so selfexec events land in
		// ~/.cerberus/cerberus.log alongside every other daemon event.
		selfexecOpts := selfexec.DefaultOptions()
		selfexecOpts.Logger = logger
		go selfexec.WatchAndExit(cmd.Context(), selfexecOpts)

		return srv.Run()
	},
}
