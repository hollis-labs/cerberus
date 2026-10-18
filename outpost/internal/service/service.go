package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"time"

	"github.com/hollis-labs/libs/app/app-host/host"
	pluginhost "github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-host"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/capability"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
	auth "github.com/hollis-labs/libs/security/auth"
)

// Listen restricts plaintext HTTP to loopback or a Tailscale address range.
// Interface ownership and encryption remain the operator's responsibility.
func Listen(address string) (net.Listener, error) {
	h, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(h)
	allowed := ip.IsLoopback() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(ip)
	if err != nil || !allowed {
		return nil, fmt.Errorf("outpost HTTP must bind a literal loopback or Tailscale address")
	}
	return net.Listen("tcp", address)
}

func Run(ctx context.Context, cfg Config, version string) error {
	ln, err := Listen(cfg.Address)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	return runWithListener(ctx, cfg, version, ln)
}

func runWithListener(ctx context.Context, cfg Config, version string, ln net.Listener) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	instance := "outpost-" + hex.EncodeToString(nonce[:])
	inv := &inventory{}
	for _, p := range cfg.Plugins {
		data := filepath.Join(cfg.StateDir, "plugins", p.ID, "data")
		cache := filepath.Join(cfg.StateDir, "plugins", p.ID, "cache")
		spec := pluginhost.Spec{ID: p.ID, Command: p.Command, Args: p.Args, Dir: p.Dir, Env: p.Env,
			Init: subprocess.InitParams{PluginDir: p.Dir, DataDir: data, CacheDir: cache, Config: p.Config,
				HostInfo:           subprocess.HostInfo{Version: version, Protocol: subprocess.ProtocolVersion},
				CapabilityContract: capability.ContractVersion,
				Incarnation:        capability.RuntimeIdentity{HostInstance: instance, OwnerID: p.ID, OwnerGeneration: 1},
				Grants:             capability.GrantSet{}}}
		inv.entries = append(inv.entries, &pluginEntry{id: p.ID, optional: p.Optional, runtime: newPluginRuntime(spec)})
	}
	if err := inv.Start(ctx); err != nil {
		return err
	}
	gate, err := auth.New(auth.Config{Mode: auth.ModeEnforce, Verifier: FileVerifier{Path: cfg.CredentialsFile}})
	if err != nil {
		_ = inv.Stop(context.Background())
		return err
	}
	life, cancel := context.WithCancel(ctx)
	defer cancel()
	routes := http.NewServeMux()
	routes.HandleFunc("GET /api/readyz", inv.Readiness)
	routes.Handle("/", gate.Middleware(AuthorizePlugins(inv.APIRoutes())))
	monitorDone := make(chan struct{})
	h, err := host.New(host.Config{Name: "outpost", Routes: routes, Stop: func(stopCtx context.Context) error {
		cancel()
		<-monitorDone
		return inv.Stop(stopCtx)
	}})
	if err != nil {
		_ = inv.Stop(context.Background())
		return err
	}
	fault := make(chan error, 1)
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-life.Done():
				return
			case <-ticker.C:
			}
			if checkErr := inv.Check(life); checkErr != nil && life.Err() == nil {
				fault <- checkErr
				cancel()
				return
			}
		}
	}()
	err = h.Serve(life, ln)
	select {
	case failure := <-fault:
		return errors.Join(err, failure)
	default:
		return err
	}
}

// AuthorizePlugins keeps per-plugin authority at the host boundary. The shared
// app-host adapter forwards no bearer credentials to child processes.
func AuthorizePlugins(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := pluginID(r.URL.Path)
		if !identifier.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		principal, ok := auth.FromContext(r.Context())
		if !ok || !principal.Verified || !principal.HasScope("outpost."+id) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "plugin scope required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
