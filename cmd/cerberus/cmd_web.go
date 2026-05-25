package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/chrispian/cerberus/internal/secrets"
	"github.com/chrispian/cerberus/internal/webui"
	"github.com/spf13/cobra"
)

var (
	webListenAddr = "127.0.0.1:4783"
	webOpen       = true
	webWait       = 20 * time.Second
)

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Run a compact local web console for v2 resources",
	Long:  "Starts a local-only web console for listing resources and running common v2 workflows such as launch, build-plus-restart, restart, and open.",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newResourceSocketClient()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), webWait)
		defer cancel()
		if err := waitForDaemon(ctx, client); err != nil {
			return fmt.Errorf("connect daemon: %w", err)
		}

		url := "http://" + webListenAddr
		srv := &http.Server{
			Addr:              webListenAddr,
			Handler:           webui.New(client, cfgPath, secrets.NewKeychainProvider(), nil).Handler(),
			ReadHeaderTimeout: 5 * time.Second,
		}

		errCh := make(chan error, 1)
		go func() {
			errCh <- srv.ListenAndServe()
		}()

		fmt.Printf("Cerberus web UI listening at %s\n", url)
		if webOpen {
			if err := openBrowser(url); err != nil {
				fmt.Printf("Open browser manually: %s (%v)\n", url, err)
			}
		}

		sigCtx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		select {
		case <-sigCtx.Done():
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			return srv.Shutdown(shutdownCtx)
		case err := <-errCh:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}
	},
}

func init() {
	webCmd.Flags().StringVar(&webListenAddr, "listen", webListenAddr, "listen address for the local web UI")
	webCmd.Flags().BoolVar(&webOpen, "open", webOpen, "open the web UI in the default browser after start")
	webCmd.Flags().DurationVar(&webWait, "wait-for-daemon", webWait, "how long to wait for the daemon socket before failing startup")
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url) //nolint:gosec // fixed browser launcher command
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec // fixed browser launcher command
	default:
		cmd = exec.Command("xdg-open", url) //nolint:gosec // fixed browser launcher command
	}
	return cmd.Start()
}

func waitForDaemon(ctx context.Context, client socketPinger) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		err := client.Ping(ctx)
		if err == nil {
			return nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return lastErr
		case <-ticker.C:
		}
	}
}

type socketPinger interface {
	Ping(ctx context.Context) error
}
