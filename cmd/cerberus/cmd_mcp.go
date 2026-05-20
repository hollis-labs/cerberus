package main

import (
	"context"
	"fmt"
	"os"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/mcp"
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
of truth for runtime execution:
- v2 resource operations go through the shared resource runtime service

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

		srv := mcp.NewServer("cerberus", "0.1.0")

		// Lifecycle + resource tools route through the socket client.
		// Every tool call forwards to the daemon, which owns the live
		// runtime layers. No per-subprocess cache -> no staleness.
		srv.RegisterTool(mcp.NewCerberusHealthTool(socketClient))

		// Project / resource / pipeline tools go through the socket too
		// so their data is consistent with what the daemon sees.
		srv.RegisterTool(mcp.NewCerberusProjectListTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceListTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceStatusTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceInspectTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceDoctorTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceLogsTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceReloadTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceStopTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceDeployTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceSyncTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceApplyTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusResourceRemoveTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusPipelineListTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusPipelineRunTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusConnectorListTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusConnectorDescribeTool(socketClient))

		// Connector-based tools (external integrations). Out of scope
		// for CERB-2 — they read secrets from keychain and hit remote
		// APIs; they don't touch service config.
		srv.RegisterTool(mcp.NewCerberusGithubStatusTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusGithubReleasesTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusGithubRunsTool(socketClient))

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
			srv.RegisterTool(mcp.NewCerberusSSHExecTool(cfg, socketClient))
			srv.RegisterTool(mcp.NewCerberusSSHStatusTool(cfg, socketClient))
		}

		// External connectors route through the daemon-owned connector boundary.
		srv.RegisterTool(mcp.NewCerberusDomainListTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusDomainStatusTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusDNSListTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusDNSCreateTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusDNSDeleteTool(socketClient))

		srv.RegisterTool(mcp.NewCerberusForgeServersTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusForgeServerTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusForgeSitesTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusForgeDeployTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusForgeExecTool(socketClient))

		srv.RegisterTool(mcp.NewCerberusCloudflareZonesTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusCloudflareDNSListTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusCloudflareDNSCreateTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusCloudflareDNSDeleteTool(socketClient))

		srv.RegisterTool(mcp.NewCerberusDockerPSTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusDockerLogsTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusDockerUpTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusDockerDownTool(socketClient))

		srv.RegisterTool(mcp.NewCerberusServerListTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusServerShowTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusServerCreateTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusServerStartTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusServerStopTool(socketClient))
		srv.RegisterTool(mcp.NewCerberusServerDestroyTool(socketClient))

		// CW-20260519-0053: selfexec.WatchAndExit removed. CERB-4 added it
		// so a binary swap triggered respawn-on-next-tool-call, assuming the
		// parent MCP host respawns dead children. That holds for Claude Code
		// but not for `mux mcp --proxy`, which leaves dead children dead.
		// `cerberus resource deploy cerberus-daemon-service` rebuilds the
		// binary on disk, so every selfexec watcher in every running
		// `cerberus mcp` child fires within 30s and exits — wiping out MCP
		// access fleet-wide. Each tool call already re-dials the daemon
		// socket, so the in-memory subprocess survives daemon restarts on
		// its own; the parent host can recycle children at its own cadence.
		return srv.Run()
	},
}
