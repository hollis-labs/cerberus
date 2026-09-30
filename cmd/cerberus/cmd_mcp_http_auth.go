package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/oauth"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// mcp-http as an OAuth 2.1 resource server (WP-S8).

// mcpHTTPAuth is mcp-http's auth when mcp-http.yaml configures an issuer.
type mcpHTTPAuth struct {
	cfg      oauth.Config
	verifier *oauth.Verifier
	caps     cerbapi.AuthCapabilities
}

type authCapabilityClient interface {
	AuthCapabilities(ctx context.Context) (cerbapi.AuthCapabilities, error)
}

// setupMCPHTTPAuth loads the auth config and checks the daemon will verify
// what mcp-http forwards: a daemon that has no /auth/capabilities (an
// older one), no auth, or another resource would let a token's scopes go
// unchecked there, so mcp-http refuses to start rather than require auth
// it cannot back.
func setupMCPHTTPAuth(ctx context.Context, client authCapabilityClient) (*mcpHTTPAuth, error) {
	cfg, err := mcpHTTPAuthConfig()
	if errors.Is(err, oauth.ErrNotConfigured) || (err == nil && !cfg.Configured()) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cerberus mcp-http did not start: %w", err)
	}
	if u, perr := url.Parse(cfg.Resource); perr == nil && u.Path != mcpHTTPPath {
		return nil, fmt.Errorf("cerberus mcp-http did not start: the resource %s serves /mcp at %q, but --path is %q", cfg.Resource, u.Path, mcpHTTPPath)
	}
	caps, err := client.AuthCapabilities(ctx)
	switch {
	case err != nil:
		return nil, redact.Guidance("cerberus mcp-http did not start: auth is configured, and the daemon could not say whether it verifies tokens (%v); an older daemon does not, so update and restart it (`cerberus daemon`), then retry", err)
	case !caps.OAuth:
		return nil, redact.Guidance("cerberus mcp-http did not start: auth is configured in %s, but the daemon has none loaded (it verifies every forwarded token itself); restart the daemon so it reads the file, and check its log for daemon.auth.config_refused", oauth.ConfigFilename)
	case caps.Resource != cfg.Resource:
		return nil, redact.Guidance("cerberus mcp-http did not start: the daemon verifies tokens for %s, and mcp-http is configured for %s; restart the daemon so both read the same file", caps.Resource, cfg.Resource)
	}
	v, err := oauth.NewVerifier(cfg, oauth.VerifierOptions{Builtin: caps.BuiltinJWKS})
	if err != nil {
		return nil, err
	}
	return &mcpHTTPAuth{cfg: cfg, verifier: v, caps: caps}, nil
}

// tokenVerifier adapts the verifier to the SDK's bearer middleware. The raw
// token rides in TokenInfo.Extra only as far as ScopeMiddleware, which
// hands it to the socket client to forward.
func (a *mcpHTTPAuth) tokenVerifier() sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		id, err := a.verifier.Verify(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", sdkauth.ErrInvalidToken, err)
		}
		return &sdkauth.TokenInfo{Scopes: id.Scopes, Expiration: id.Expiry, UserID: id.Issuer + "#" + id.Subject,
			Extra: map[string]any{mcp.TokenKey: token, "client": id.Client}}, nil
	}
}

// metadataURL is the RFC 9728 document for the resource: the well-known
// path with the resource's path appended, at the resource's origin.
func (a *mcpHTTPAuth) metadataURL() string {
	u, _ := url.Parse(a.cfg.Resource)
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + u.Path
}

func (a *mcpHTTPAuth) authorizationServers() []string {
	var out []string
	if a.cfg.Builtin {
		out = append(out, a.cfg.BuiltinIssuer())
	}
	for _, is := range a.cfg.Issuers {
		out = append(out, strings.TrimSuffix(is.Issuer, "/"))
	}
	return out
}

// mount serves the protected /mcp handler and the metadata documents.
func (a *mcpHTTPAuth) mount(mux *http.ServeMux, path string, mcpHandler http.Handler) {
	mux.Handle(path, sdkauth.RequireBearerToken(a.tokenVerifier(), &sdkauth.RequireBearerTokenOptions{
		ResourceMetadataURL: a.metadataURL(), ClockSkew: time.Minute,
	})(mcpHandler))
	prm := sdkauth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               a.cfg.Resource,
		AuthorizationServers:   a.authorizationServers(),
		ScopesSupported:        oauth.Supported,
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Cerberus",
		ResourceDocumentation:  "https://github.com/hollis-labs/cerberus/blob/main/docs/mcp-http.md",
	})
	u, _ := url.Parse(a.cfg.Resource)
	mux.Handle("/.well-known/oauth-protected-resource", prm)
	mux.Handle("/.well-known/oauth-protected-resource"+u.Path, prm)
	if a.cfg.Builtin && a.caps.BuiltinJWKS != nil {
		iss := a.cfg.BuiltinIssuer()
		// The built-in issuer mints no tokens over HTTP: there is no
		// authorization or token endpoint, and no registration. Its
		// metadata says where its keys are, and how tokens are made.
		meta := map[string]any{
			"issuer":                                iss,
			"jwks_uri":                              iss + "/.well-known/jwks.json",
			"scopes_supported":                      oauth.Supported,
			"response_types_supported":              []string{},
			"grant_types_supported":                 []string{},
			"token_endpoint_auth_methods_supported": []string{"none"},
			"service_documentation":                 "tokens are issued by the operator on a terminal: cerberus mcp-http token issue --client <name> --scope <scopes>",
		}
		mux.HandleFunc("/.well-known/oauth-authorization-server", jsonHandler(meta))
		mux.HandleFunc("/.well-known/jwks.json", jsonHandler(a.caps.BuiltinJWKS))
	}
}

func jsonHandler(v any) http.HandlerFunc {
	data, _ := json.Marshal(v)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}
}

// certReloader serves the operator's certificate, read again when either
// file changes, so a renewal needs no restart.
type certReloader struct {
	certFile, keyFile string

	mu    sync.Mutex
	stamp string
	cert  *tls.Certificate
}

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if _, err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) load() (*tls.Certificate, error) {
	stamp := ""
	for _, f := range []string{r.certFile, r.keyFile} {
		info, err := os.Stat(f)
		if err != nil {
			return nil, fmt.Errorf("read the TLS certificate: %w", err)
		}
		stamp += fmt.Sprintf("%d:%d;", info.Size(), info.ModTime().UnixNano())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cert != nil && stamp == r.stamp {
		return r.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		if r.cert != nil {
			// A half-written renewal keeps the old certificate serving.
			return r.cert, nil
		}
		return nil, fmt.Errorf("load the TLS certificate %s: %w", r.certFile, err)
	}
	r.cert, r.stamp = &cert, stamp
	return r.cert, nil
}

func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.load()
}

// certHosts are the names and addresses a certificate is valid for, which
// the Host allow-list accepts.
func certHosts(certFile string) ([]string, error) {
	data, err := os.ReadFile(certFile) //nolint:gosec // the operator's certificate path
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("the TLS certificate is not PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	hosts := append([]string(nil), leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		hosts = append(hosts, ip.String())
	}
	return hosts, nil
}
