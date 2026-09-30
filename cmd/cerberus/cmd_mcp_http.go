package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/loopback"
	"github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/service"
	gmcp "github.com/hollis-labs/go-mcp/server"
	httptransport "github.com/hollis-labs/go-mcp/transport/http"
	"github.com/spf13/cobra"
)

var (
	mcpHTTPListen   = "127.0.0.1:4785"
	mcpHTTPPath     = "/mcp"
	mcpHTTPOrigins  []string
	mcpHTTPInsecure bool
	mcpHTTPHosts    []string
	mcpHTTPTLSCert  string
	mcpHTTPTLSKey   string
	// mcpHTTPAuditSink is where --insecure-listen is recorded. Tests swap it.
	mcpHTTPAuditSink = app.AuditSink
)

var mcpHTTPCmd = &cobra.Command{
	Use:   "mcp-http",
	Short: "MCP server over HTTP",
	Long: `Starts a local HTTP MCP endpoint backed by the Cerberus daemon.

This serves the same Cerberus MCP tool surface as 'cerberus mcp', but over
HTTP for local MCP clients that do not speak stdio.

With no auth configured it is loopback-only: --listen must be 127.0.0.1,
localhost or [::1], and a request whose Host header is not a loopback name is
refused. An SSH local forward or Tailscale onto a local port is the easy way to
reach it from elsewhere.

With ~/.cerberus/mcp-http.yaml configuring an issuer (see docs/mcp-http.md) it
is an OAuth 2.1 resource server: every call needs a bearer token bound to its
resource URL, on loopback too. Off loopback it also needs TLS (--tls-cert and
--tls-key, or tls: in the file); the certificate's names join the Host
allow-list. Tokens come from the built-in issuer (cerberus mcp-http token
issue) or a configured external one.

Under the permissive posture only, --insecure-listen accepts a non-loopback
--listen with no auth, and --allow-host names the hostnames and addresses
clients reach it by. Anyone who can reach that address can call every tool it
serves. It prints a warning at start and is recorded in the audit log before it
listens. The web console is loopback-only in every posture.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		service.InitLifecycleLog()
		logger := service.GetLogger()

		sockPath, err := cerbapi.SocketPath()
		if err != nil {
			return fmt.Errorf("resolve socket path: %w", err)
		}
		// An HTTP MCP endpoint serves agents. Several clients may share it,
		// so the client is named per call, from the clientInfo each call
		// carries.
		// A verified caller's token is forwarded, and the daemon checks it
		// again before the call gets a verified principal (WP-S8).
		socketClient := cerbapi.NewSocketClient(sockPath, cerbapi.WithClientLogger(logger),
			cerbapi.WithPrincipalClaim(mcpPrincipal(cerbapi.ViaMCPHTTP, nil)), cerbapi.WithBearer(mcp.BearerFromContext))

		pingCtx, cancel := context.WithTimeout(cmd.Context(), cerbapi.DialTimeout)
		if pingErr := socketClient.Ping(pingCtx); pingErr != nil {
			logger.Warn("client.mcp_http.daemon_unreachable_on_boot",
				"path", sockPath,
				"error", pingErr.Error(),
				"message", "HTTP MCP endpoint will keep running; tool calls will fail until cerberus daemon is started")
			fmt.Fprintf(os.Stderr, "WARN: cerberus daemon not reachable at %s; start it with 'cerberus daemon' for tool calls to succeed.\n", sockPath)
		}
		cancel()

		authCtx, cancelAuth := context.WithTimeout(cmd.Context(), cerbapi.DialTimeout)
		auth, err := setupMCPHTTPAuth(authCtx, socketClient)
		cancelAuth()
		if err != nil {
			return err
		}
		certFile, keyFile := mcpHTTPTLSCert, mcpHTTPTLSKey
		if auth != nil && certFile == "" && keyFile == "" {
			certFile, keyFile = auth.cfg.TLS.Cert, auth.cfg.TLS.Key
		}
		if (certFile == "") != (keyFile == "") {
			return errors.New("cerberus mcp-http: TLS needs both --tls-cert and --tls-key")
		}
		var certs *certReloader
		if certFile != "" {
			if certs, err = newCertReloader(certFile, keyFile); err != nil {
				return err
			}
		}
		if err = checkMCPHTTPListen(auth != nil, certs != nil); err != nil {
			return err
		}

		ln, err := net.Listen("tcp", mcpHTTPListen)
		if err != nil {
			return fmt.Errorf("listen %s: %w", mcpHTTPListen, err)
		}
		guard, err := loopback.NewGuardForAddr(mcpHTTPListen, ln.Addr(), mcpHTTPOrigins...)
		if err != nil {
			_ = ln.Close()
			return err
		}
		if certs != nil {
			hosts, herr := certHosts(certFile)
			if herr != nil {
				_ = ln.Close()
				return herr
			}
			guard.AllowHosts(hosts...)
			guard.AllowHosts(mcpHTTPHosts...)
			if auth != nil {
				if u, perr := url.Parse(auth.cfg.Resource); perr == nil {
					guard.AllowHosts(u.Hostname())
				}
			}
		}
		if mcpHTTPInsecure {
			guard.AllowHosts(mcpHTTPHosts...)
			if err = cerbapi.RecordInsecureListen(inProcessContext(cmd.Context()), mcpHTTPAuditSink(), "mcp-http", mcpHTTPListen, mcpHTTPHosts); err != nil {
				_ = ln.Close()
				return fmt.Errorf("mcp-http did not start: --insecure-listen is recorded in the audit log before it listens, and the record could not be written: %w", err)
			}
			fmt.Fprint(os.Stderr, insecureListenWarning(mcpHTTPListen))
		}

		var serverOpts []mcp.Option
		if auth != nil {
			// Scopes are checked here as well as in the daemon, so a call
			// a token does not cover is never forwarded.
			serverOpts = append(serverOpts, gmcp.WithReceivingMiddleware(mcp.ScopeMiddleware))
		}
		mcpServer := buildCerberusMCPServer(socketClient, serverOpts...)
		startPluginToolSync(cmd.Context(), mcpServer, socketClient, logger)
		httpServer := &http.Server{
			Handler:           mcpHTTPHandler(mcpServer, mcpHTTPPath, guard, auth),
			ReadHeaderTimeout: 5 * time.Second,
		}
		scheme := "http"
		if certs != nil {
			scheme = "https"
			httpServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certs.GetCertificate}
		}

		errCh := make(chan error, 1)
		go func() {
			if certs != nil {
				errCh <- httpServer.ServeTLS(ln, "", "")
				return
			}
			errCh <- httpServer.Serve(ln)
		}()

		fmt.Printf("Cerberus MCP HTTP listening at %s://%s%s\n", scheme, mcpHTTPListen, mcpHTTPPath)
		if auth != nil {
			fmt.Printf("OAuth resource %s: a bearer token is required (%s)\n", auth.cfg.Resource, strings.Join(auth.authorizationServers(), ", "))
		}

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

// checkMCPHTTPListen is the listen guard. Loopback is the default. Off
// loopback needs auth and TLS, in any posture; without auth, only the
// permissive posture's --insecure-listen reaches off loopback (section 13).
// A scoped posture rule never reaches it.
func checkMCPHTTPListen(auth, tlsOn bool) error {
	if mcpHTTPInsecure {
		if auth {
			return errors.New("--insecure-listen is for running with no auth; auth is configured, so serve off loopback with --tls-cert and --tls-key instead")
		}
		if currentPosture().Global != policy.PosturePermissive {
			return errInsecureListenNeedsPermissive
		}
		if _, _, err := net.SplitHostPort(mcpHTTPListen); err != nil {
			return fmt.Errorf("cerberus mcp-http: invalid --listen %q: %w", mcpHTTPListen, err)
		}
		return nil
	}
	loopbackErr := loopback.CheckListen("cerberus mcp-http", mcpHTTPListen)
	switch {
	case loopbackErr == nil:
		if len(mcpHTTPHosts) > 0 && !tlsOn {
			return errors.New("--allow-host is for a listener off loopback; a loopback listener already accepts localhost, 127.0.0.1 and [::1]")
		}
		return nil
	case auth && tlsOn:
		return nil
	case auth:
		return errListenNeedsTLS
	}
	return errListenNeedsAuth
}

var (
	errListenNeedsTLS  = redact.Guidance("cerberus mcp-http off loopback needs TLS, because a bearer token must not cross a network in plaintext: pass --tls-cert and --tls-key (or tls: in ~/.cerberus/mcp-http.yaml), or keep it on loopback and reach it with an SSH local forward or Tailscale")
	errListenNeedsAuth = redact.Guidance("cerberus mcp-http off loopback needs auth and TLS: configure ~/.cerberus/mcp-http.yaml (docs/mcp-http.md) and pass --tls-cert and --tls-key; or keep it on loopback and reach it with an SSH local forward or Tailscale")
)

var errInsecureListenNeedsPermissive = redact.Guidance("--insecure-listen is allowed only under the permissive posture, and the posture is secure; keep mcp-http on loopback and reach it with an SSH local forward, or run `cerberus posture set permissive` in a terminal first")

func insecureListenWarning(listen string) string {
	return fmt.Sprintf("\nWARNING: cerberus mcp-http is listening on %s, off loopback, with NO authentication.\n"+
		"Anyone who can reach that address can call every tool it serves. This is allowed because the posture is permissive,\n"+
		"and it is recorded in the audit log. Stop it, or return to loopback, when you no longer need it.\n\n", listen)
}

// mcpHTTPHandler wires the MCP endpoint and /health behind guard. go-mcp's
// own origin list is given the guard's set, which is never empty: an empty
// AllowedOrigins means "every origin" to go-mcp.
func mcpHTTPHandler(server *mcp.Server, path string, guard *loopback.Guard, auth *mcpHTTPAuth) http.Handler {
	mux := http.NewServeMux()
	handler := httptransport.NewHandler(server, httptransport.HandlerOptions{
		AllowedOrigins: guard.Origins(),
	})
	if auth != nil {
		auth.mount(mux, path, handler)
	} else {
		mux.Handle(path, handler)
	}
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return guard.Middleware(mux)
}

func init() {
	mcpHTTPCmd.Flags().StringVar(&mcpHTTPListen, "listen", mcpHTTPListen, "listen address for the HTTP MCP endpoint; loopback unless auth and TLS are configured")
	mcpHTTPCmd.Flags().StringVar(&mcpHTTPTLSCert, "tls-cert", "", "TLS certificate (PEM, full chain) to serve; required off loopback")
	mcpHTTPCmd.Flags().StringVar(&mcpHTTPTLSKey, "tls-key", "", "TLS private key (PEM) for --tls-cert")
	mcpHTTPCmd.Flags().StringVar(&mcpHTTPPath, "path", mcpHTTPPath, "HTTP path for the MCP endpoint")
	mcpHTTPCmd.Flags().BoolVar(&mcpHTTPInsecure, "insecure-listen", false, "under the permissive posture only: accept a non-loopback --listen, with no authentication; warned and audited")
	mcpHTTPCmd.Flags().StringSliceVar(&mcpHTTPHosts, "allow-host", nil, "off loopback: more host names or addresses clients reach the endpoint by (Host header), beyond the certificate's")
	mcpHTTPCmd.Flags().StringSliceVar(&mcpHTTPOrigins, "allow-origin", mcpHTTPOrigins, "additional exact Origin values (scheme://host:port) for browser-based HTTP MCP requests; loopback origins on the listen port are always allowed")
}
