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

	Store    domain.Store
	Registry *connector.Registry
	Secrets  domain.SecretProvider
	Local    *localconn.Connector

	// Services is the v1-compatible service list used by existing CLI commands,
	// TUI, MCP, and daemon code. This bridges the gap during migration —
	// existing code operates on []*service.ManagedService while new code
	// operates through the connector interface.
	Services []*service.ManagedService

	storePath string
}

// New creates an App from a config file path. It loads config (v1 or v2),
// creates the connector registry with the local connector, and initializes
// the v1-compatible service list.
//
// The SQLite store is NOT opened here — call OpenStore() explicitly when
// needed. This keeps the default path (local services via config) lightweight.
func New(cfgPath string) (*App, error) {
	service.InitLifecycleLog()

	// Load v1 config (existing path — used by Services)
	v1, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// Load unified v2 config
	v2, err := config.LoadUnified(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load unified config: %w", err)
	}

	// Create v1-compatible service list
	services := service.NewFromConfig(v1)

	// Set up connector registry with local connector
	registry := connector.NewRegistry()
	local := localconn.New()

	// Register all services with the local connector
	for _, svc := range services {
		local.Register(svc)
	}
	registry.Register(local)

	// Secrets provider
	sec := secrets.NewKeychainProvider()

	home, _ := os.UserHomeDir()
	storePath := filepath.Join(home, ".cerberus", "cerberus.db")

	return &App{
		Config:    v2,
		V1Config:  v1,
		Store:     nil, // lazily opened
		Registry:  registry,
		Secrets:   sec,
		Local:     local,
		Services:  services,
		storePath: storePath,
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
