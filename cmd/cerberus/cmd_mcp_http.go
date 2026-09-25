package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/loopback"
	"github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/service"
	httptransport "github.com/hollis-labs/go-mcp/transport/http"
	"github.com/spf13/cobra"
)

var (
	mcpHTTPListen  = "127.0.0.1:4785"
	mcpHTTPPath    = "/mcp"
	mcpHTTPOrigins []string
)

var mcpHTTPCmd = &cobra.Command{
	Use:   "mcp-http",
	Short: "MCP server over HTTP",
	Long: `Starts a local HTTP MCP endpoint backed by the Cerberus daemon.

This serves the same Cerberus MCP tool surface as 'cerberus mcp', but over
HTTP for local MCP clients that do not speak stdio.

The endpoint performs no authentication yet (WP-S8 in
docs/plans/agent-authority-and-secrets.md), so it is loopback-only: --listen
must be 127.0.0.1, localhost or [::1], and a request whose Host header is not
a loopback name is refused. An SSH local forward onto any local port works;
a tunnel or reverse proxy that forwards a public hostname does not.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loopback.CheckListen("cerberus mcp-http", mcpHTTPListen); err != nil {
			return err
		}
		service.InitLifecycleLog()
		logger := service.GetLogger()

		sockPath, err := cerbapi.SocketPath()
		if err != nil {
			return fmt.Errorf("resolve socket path: %w", err)
		}
		socketClient := cerbapi.NewSocketClient(sockPath, cerbapi.WithClientLogger(logger))

		pingCtx, cancel := context.WithTimeout(cmd.Context(), cerbapi.DialTimeout)
		if pingErr := socketClient.Ping(pingCtx); pingErr != nil {
			logger.Warn("client.mcp_http.daemon_unreachable_on_boot",
				"path", sockPath,
				"error", pingErr.Error(),
				"message", "HTTP MCP endpoint will keep running; tool calls will fail until cerberus daemon is started")
			fmt.Fprintf(os.Stderr, "WARN: cerberus daemon not reachable at %s; start it with 'cerberus daemon' for tool calls to succeed.\n", sockPath)
		}
		cancel()

		ln, err := net.Listen("tcp", mcpHTTPListen)
		if err != nil {
			return fmt.Errorf("listen %s: %w", mcpHTTPListen, err)
		}
		guard, err := loopback.NewGuardForAddr(mcpHTTPListen, ln.Addr(), mcpHTTPOrigins...)
		if err != nil {
			_ = ln.Close()
			return err
		}

		httpServer := &http.Server{
			Handler:           mcpHTTPHandler(buildCerberusMCPServer(socketClient, logger), mcpHTTPPath, guard),
			ReadHeaderTimeout: 5 * time.Second,
		}

		errCh := make(chan error, 1)
		go func() {
			errCh <- httpServer.Serve(ln)
		}()

		fmt.Printf("Cerberus MCP HTTP listening at http://%s%s\n", mcpHTTPListen, mcpHTTPPath)

		sigCtx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		select {
		case <-sigCtx.Done():
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			return httpServer.Shutdown(shutdownCtx)
		case err := <-errCh:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}
	},
}

// mcpHTTPHandler wires the MCP endpoint and /health behind guard. go-mcp's
// own origin list is given the guard's set, which is never empty: an empty
// AllowedOrigins means "every origin" to go-mcp.
func mcpHTTPHandler(server *mcp.Server, path string, guard *loopback.Guard) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(path, httptransport.NewHandler(server, httptransport.HandlerOptions{
		AllowedOrigins: guard.Origins(),
	}))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return guard.Middleware(mux)
}

func init() {
	mcpHTTPCmd.Flags().StringVar(&mcpHTTPListen, "listen", mcpHTTPListen, "listen address for the HTTP MCP endpoint; must be loopback (127.0.0.1, localhost or [::1]) because the endpoint has no authentication yet")
	mcpHTTPCmd.Flags().StringVar(&mcpHTTPPath, "path", mcpHTTPPath, "HTTP path for the MCP endpoint")
	mcpHTTPCmd.Flags().StringSliceVar(&mcpHTTPOrigins, "allow-origin", mcpHTTPOrigins, "additional exact Origin values (scheme://host:port) for browser-based HTTP MCP requests; loopback origins on the listen port are always allowed")
}
