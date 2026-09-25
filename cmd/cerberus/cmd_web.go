package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/hollis-labs/cerberus/internal/app"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/hollis-labs/cerberus/internal/loopback"
	"github.com/hollis-labs/cerberus/internal/webui"
	"github.com/spf13/cobra"
)

var (
	webListenAddr  = "127.0.0.1:4783"
	webOpen        = true
	webWait        = 20 * time.Second
	webSessionIdle = webui.DefaultSessionIdle
	webOpenBrowser = true
)

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Run a compact local web console for v2 resources",
	Long: `Starts a local-only web console for listing resources and running common v2 workflows such as launch, build-plus-restart, restart, and open.

The console needs a sign-in. On start it prints (and, with --open, opens) a
one-time sign-in URL, good for two minutes; visiting it gives the browser a
session that ends after --session-idle without use, twelve hours at most, on
logout, or when this command exits. Run ` + "`cerberus web open`" + ` for another link.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loopback.CheckListen("cerberus web", webListenAddr); err != nil {
			return err
		}
		client, err := newResourceSocketClient()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), webWait)
		defer cancel()
		if err := waitForDaemon(ctx, client); err != nil {
			return fmt.Errorf("connect daemon: %w (try `cerberus daemon status` to check; if not installed, run `cerberus install`)", err)
		}

		webSrv, err := webui.New(client, app.AuditSink(), cfgPath, app.ConnectorSecrets(cfgPath), nil)
		if err != nil {
			return fmt.Errorf("init web ui: %w", err)
		}
		webSrv.SetSessionLimits(0, webSessionIdle, 0)
		webSrv.SetPosture(currentPosture)

		ln, err := net.Listen("tcp", webListenAddr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", webListenAddr, err)
		}
		guard, err := loopback.NewGuardForAddr(webListenAddr, ln.Addr())
		if err != nil {
			_ = ln.Close()
			return err
		}

		url := "http://" + webListenAddr
		srv := &http.Server{
			Handler:           webSrv.Handler(guard),
			ReadHeaderTimeout: 5 * time.Second,
		}

		errCh := make(chan error, 1)
		go func() {
			errCh <- srv.Serve(ln)
		}()

		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home for the web login key: %w", err)
		}
		removeKey, err := webSrv.WriteLoginKey(webui.LoginKeyPath(home, webListenAddr), url)
		if err != nil {
			return err
		}
		defer removeKey()
		loginURL, err := webSrv.LoginURL(url)
		if err != nil {
			return err
		}

		fmt.Printf("Cerberus web UI listening at %s\n", url)
		fmt.Printf("Sign in (one-time link, valid %s): %s\n", webui.DefaultLoginTTL, loginURL)
		if webOpen {
			if err := openBrowser(loginURL); err != nil {
				fmt.Printf("Open it in your browser manually (%v)\n", err)
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

// webOpenCmd is a new sign-in link for a running console: a session ended,
// the first link expired, or a second browser.
var webOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Print and open a one-time sign-in link for the running web console",
	Long: `Mints a one-time sign-in URL for the ` + "`cerberus web`" + ` listening on --listen,
from the key that console keeps in ~/.cerberus/web (readable only by you), prints
it, and opens it unless --browser=false. The link is good for two minutes and
for one sign-in.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		loginURL, err := webui.MintLoginURL(webui.LoginKeyPath(home, webListenAddr))
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Sign in (one-time link, valid %s): %s\n", webui.DefaultLoginTTL, loginURL)
		if webOpenBrowser {
			if err := openBrowser(loginURL); err != nil {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Open it in your browser manually (%v)\n", err)
			}
		}
		return nil
	},
}

func init() {
	webCmd.PersistentFlags().StringVar(&webListenAddr, "listen", webListenAddr, "listen address for the local web UI; must be loopback (127.0.0.1, localhost or [::1])")
	webCmd.Flags().BoolVar(&webOpen, "open", webOpen, "open the one-time sign-in link in the default browser after start")
	webCmd.Flags().DurationVar(&webWait, "wait-for-daemon", webWait, "how long to wait for the daemon socket before failing startup")
	webCmd.Flags().DurationVar(&webSessionIdle, "session-idle", webSessionIdle, "sign a browser out after this long without a request")
	webOpenCmd.Flags().BoolVar(&webOpenBrowser, "browser", webOpenBrowser, "open the link in the default browser as well as printing it")
	webCmd.AddCommand(webOpenCmd)
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
