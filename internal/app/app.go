package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/connector"
	cloudflareconn "github.com/chrispian/cerberus/internal/connector/cloudflare"
	doconn "github.com/chrispian/cerberus/internal/connector/digitalocean"
	dockerconn "github.com/chrispian/cerberus/internal/connector/docker"
	forgeconn "github.com/chrispian/cerberus/internal/connector/forge"
	githubconn "github.com/chrispian/cerberus/internal/connector/github"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	namecheapconn "github.com/chrispian/cerberus/internal/connector/namecheap"
	sshconn "github.com/chrispian/cerberus/internal/connector/ssh"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/registry"
	"github.com/chrispian/cerberus/internal/secrets"
	"github.com/chrispian/cerberus/internal/store/sqlite"
)

// App is the central dependency container for Cerberus. It wires together
// config, store, connector registry, and secrets provider.
type App struct {
	Config *config.ConfigV2

	Store    domain.Store
	Registry *connector.Registry
	Secrets  domain.SecretProvider
	Local    *localconn.Connector
	Runtime  *cerbapi.ResourceRuntimeService
	External *cerbapi.ExternalConnectorService

	storePath string
}

// New creates an App from a config file path. It loads the unified v2 config,
// creates the connector registry with the local connector, and initializes
// the service registry backed by a file-based config Source.
//
// The SQLite store is NOT opened here — call OpenStore() explicitly when
// needed. This keeps the default path (local services via config) lightweight.
func New(cfgPath string) (*App, error) {
	// Assemble the effective v2 config: every registered project config
	// merged over the optional global config.yaml at cfgPath.
	v2, err := registry.ResolveConfig(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	registry, sec := newConnectorRegistry()
	local := localconn.New()
	registry.Register(local)

	runtime := cerbapi.NewResourceRuntimeService(
		cerbapi.WithResourceRuntimeLogger(nil),
		cerbapi.WithResourceRuntimeLocalConnector(local),
		cerbapi.WithResourceRuntimeConfigV2(v2),
		cerbapi.WithResourceRuntimeConfigPath(cfgPath),
	)
	external := cerbapi.NewExternalConnectorService(registry)

	home, _ := os.UserHomeDir()
	storePath := filepath.Join(home, ".cerberus", "cerberus.db")

	return &App{
		Config:    v2,
		Store:     nil, // lazily opened
		Registry:  registry,
		Secrets:   sec,
		Local:     local,
		Runtime:   runtime,
		External:  external,
		storePath: storePath,
	}, nil
}

func NewExternalConnectorService() *cerbapi.ExternalConnectorService {
	registry, _ := newConnectorRegistry()
	return cerbapi.NewExternalConnectorService(registry)
}

func newConnectorRegistry() (*connector.Registry, domain.SecretProvider) {
	sec := secrets.NewKeychainProvider()
	registry := connector.NewRegistry()
	registerBuiltInConnectors(registry, sec)
	return registry, sec
}

func registerBuiltInConnectors(registry *connector.Registry, sec domain.SecretProvider) {
	registry.RegisterDefinition(cloudflareconn.Definition())
	registry.RegisterDefinition(doconn.Definition())
	registry.RegisterDefinition(dockerconn.Definition())
	registry.RegisterDefinition(forgeconn.Definition())
	registry.RegisterDefinition(githubconn.Definition())
	registry.RegisterDefinition(namecheapconn.Definition())
	registry.RegisterDefinition(sshconn.Definition())

	if cloudflare, err := cloudflareconn.New(sec); err == nil {
		registry.Register(cloudflare)
	} else {
		registry.RegisterUnavailable("cloudflare", err)
	}
	if digitalocean, err := doconn.New(sec); err == nil {
		registry.Register(digitalocean)
	} else {
		registry.RegisterUnavailable("digitalocean", err)
	}

	if docker, err := dockerconn.New(); err == nil {
		registry.Register(docker)
	} else {
		registry.RegisterUnavailable("docker", err)
	}
	if forge, err := forgeconn.New(sec); err == nil {
		registry.Register(forge)
	} else {
		registry.RegisterUnavailable("forge", err)
	}
	if github, err := githubconn.New(sec); err == nil {
		registry.Register(github)
	} else {
		registry.RegisterUnavailable("github", err)
	}
	if namecheap, err := namecheapconn.New(sec); err == nil {
		registry.Register(namecheap)
	} else {
		registry.RegisterUnavailable("namecheap", err)
	}
	registry.Register(sshconn.New(sec))
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
