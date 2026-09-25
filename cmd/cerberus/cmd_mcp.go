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
		startPluginToolSync(cmd.Context(), srv, socketClient, logger)

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

// startPluginToolSync serves the generated tools for the plugin operations the
// operator exposed in connector-config.yaml, refreshed every
// mcp.PluginToolRefreshInterval so a load, unload or config change reaches the
// client (via tools/list_changed) without restarting anything.
func startPluginToolSync(ctx context.Context, srv *mcp.Server, client cerbapi.Client, logger *slog.Logger) {
	mcp.ServePluginTools(ctx, srv, client, func(format string, args ...any) {
		logger.Info("client.mcp.plugin_tools", "message", fmt.Sprintf(format, args...))
	})
}

func buildCerberusMCPServer(socketClient cerbapi.Client, logger *slog.Logger) *mcp.Server {
	srv := mcp.NewServer("cerberus", "0.1.0")
	for _, tool := range mcp.AllTools(socketClient) {
		srv.RegisterTool(tool)
	}
	return srv
}
