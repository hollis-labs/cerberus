package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/connector"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/secrets"
	"github.com/chrispian/cerberus/internal/service"
	"github.com/chrispian/cerberus/internal/store/sqlite"
)

// App is the central dependency container for Cerberus. It wires together
// config, store, connector registry, and secrets provider.
type App struct {
	Config   *config.ConfigV2
	V1Config *config.Config // retained for backward compat with existing code paths

	// Source is the live config source for long-running processes.
	// Every lifecycle op should re-read through it (typically via
	// ServiceRegistry.Reload()).
	Source config.Source

	Store    domain.Store
	Registry *connector.Registry
	Secrets  domain.SecretProvider
	Local    *localconn.Connector

	// ServiceRegistry owns the live []*ManagedService list inside the
	// daemon / MCP process. It re-parses config via Source on every
	// Reload() and on SIGHUP / file-watcher events. Callers that need the
	// current service slice should use Registry.Current().
	ServiceRegistry *service.ServiceRegistry

	storePath string
}

// New creates an App from a config file path. It loads config (v1 or v2),
// creates the connector registry with the local connector, and initializes
// the service registry backed by a file-based config Source.
//
// The SQLite store is NOT opened here — call OpenStore() explicitly when
// needed. This keeps the default path (local services via config) lightweight.
func New(cfgPath string) (*App, error) {
	service.InitLifecycleLog()

	// Load v1 config (existing path — used by connectors).
	v1, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// Load unified v2 config.
	v2, err := config.LoadUnified(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load unified config: %w", err)
	}

	// Build the live config source and service registry. The registry
	// owns the canonical []*ManagedService list; every reload flows
	// through it so daemon + MCP handlers always see fresh-from-disk
	// definitions.
	src := config.NewFileSource(cfgPath)
	sreg, err := service.NewServiceRegistry(src, service.GetLogger())
	if err != nil {
		return nil, fmt.Errorf("init service registry: %w", err)
	}

	// Set up connector registry with local connector.
	registry := connector.NewRegistry()
	local := localconn.New()

	// Register all services with the local connector. The local
	// connector holds its own Resource->ManagedService map; we seed it
	// with the initial set from the registry.
	for _, svc := range sreg.Current() {
		local.Register(svc)
	}
	registry.Register(local)

	// Secrets provider
	sec := secrets.NewKeychainProvider()

	home, _ := os.UserHomeDir()
	storePath := filepath.Join(home, ".cerberus", "cerberus.db")

	return &App{
		Config:          v2,
		V1Config:        v1,
		Source:          src,
		Store:           nil, // lazily opened
		Registry:        registry,
		Secrets:         sec,
		Local:           local,
		ServiceRegistry: sreg,
		storePath:       storePath,
	}, nil
}

// OpenStore opens the SQLite store, creating it if necessary.
// This is separate from New() so that users who only use local services
// via config don't pay the cost of a database file.
func (a *App) OpenStore() error {
	if a.Store != nil {
		return nil
	}

	// Ensure directory exists
	dir := filepath.Dir(a.storePath)
	if err := os.MkdirAll(dir, 0755); err != nil { //nolint:gosec
		return fmt.Errorf("create store directory: %w", err)
	}

	store, err := sqlite.Open(a.storePath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	a.Store = store
	return nil
}

// Close releases all resources held by the App.
func (a *App) Close() error {
	if a.Store != nil {
		return a.Store.Close()
	}
	return nil
}
