package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/service"
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

		srv := buildCerberusMCPServer(socketClient, logger)

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
		return srv.Run(cmd.Context())
	},
}

func buildCerberusMCPServer(socketClient cerbapi.Client, logger *slog.Logger) *mcp.Server {
	srv := mcp.NewServer("cerberus", "0.1.0")

	srv.RegisterTool(mcp.NewCerberusHealthTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusProjectListTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceListTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceStatusTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceInspectTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceDoctorTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceLogsTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceReloadTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceStopTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceDeployTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceEnsureFreshTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceSyncTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceApplyTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusResourceRemoveTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusPipelineListTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusPipelineRunTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusConnectorListTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusConnectorDescribeTool(socketClient))

	srv.RegisterTool(mcp.NewCerberusGithubStatusTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusGithubReleasesTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusGithubRunsTool(socketClient))

	cfg, cfgErr := loadUnifiedForTools(cfgPath)
	if cfgErr != nil {
		logger.Warn("client.mcp.config_load_failed",
			"path", cfgPath,
			"error", cfgErr.Error(),
			"message", "SSH tools will be unavailable; daemon-routed tools still work")
	} else {
		srv.RegisterTool(mcp.NewCerberusSSHExecTool(cfg, socketClient))
		srv.RegisterTool(mcp.NewCerberusSSHStatusTool(cfg, socketClient))
		srv.RegisterTool(mcp.NewCerberusSSHPutTool(cfg, socketClient))
		srv.RegisterTool(mcp.NewCerberusSSHGetTool(cfg, socketClient))
		srv.RegisterTool(mcp.NewCerberusSSHPutDirTool(cfg, socketClient))
		srv.RegisterTool(mcp.NewCerberusSSHGetDirTool(cfg, socketClient))
	}

	srv.RegisterTool(mcp.NewCerberusDomainListTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusDomainStatusTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusNameserversSetTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusDNSListTool(socketClient))
	for _, tool := range mcp.NewCerberusDNSRecordSetTools(socketClient) {
		srv.RegisterTool(tool)
	}
	srv.RegisterTool(mcp.NewCerberusDNSCreateTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusDNSDeleteTool(socketClient))

	srv.RegisterTool(mcp.NewCerberusForgeServersTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusForgeServerTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusForgeSitesTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusForgeDeployTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusForgeExecTool(socketClient))

	srv.RegisterTool(mcp.NewCerberusCloudflareZonesTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusCloudflareZoneCreateTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusCloudflareDNSListTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusCloudflareDNSCreateTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusCloudflareDNSDeleteTool(socketClient))

	srv.RegisterTool(mcp.NewCerberusDockerPSTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusDockerLogsTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusDockerUpTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusDockerDownTool(socketClient))

	srv.RegisterTool(mcp.NewCerberusDropletListTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusDropletGetTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusDropletCreateTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusDropletStartTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusDropletStopTool(socketClient))
	srv.RegisterTool(mcp.NewCerberusDropletDestroyTool(socketClient))

	return srv
}
