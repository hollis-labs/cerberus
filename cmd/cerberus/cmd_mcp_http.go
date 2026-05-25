package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/service"
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
HTTP so remote MCP clients and tunnels can connect to it.`,
	RunE: func(cmd *cobra.Command, args []string) error {
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

		mcpServer := buildCerberusMCPServer(socketClient, logger)
		mux := http.NewServeMux()
		mux.Handle(mcpHTTPPath, httptransport.NewHandler(mcpServer, httptransport.HandlerOptions{
			AllowedOrigins: mcpHTTPOrigins,
		}))
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
		})

		httpServer := &http.Server{
			Addr:              mcpHTTPListen,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}

		errCh := make(chan error, 1)
		go func() {
			errCh <- httpServer.ListenAndServe()
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

func init() {
	mcpHTTPCmd.Flags().StringVar(&mcpHTTPListen, "listen", mcpHTTPListen, "listen address for the HTTP MCP endpoint")
	mcpHTTPCmd.Flags().StringVar(&mcpHTTPPath, "path", mcpHTTPPath, "HTTP path for the MCP endpoint")
	mcpHTTPCmd.Flags().StringSliceVar(&mcpHTTPOrigins, "allow-origin", mcpHTTPOrigins, "allowed Origin values for browser-based HTTP MCP requests")
}
