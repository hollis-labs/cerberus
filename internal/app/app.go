package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hollis-labs/go-apppaths/paths"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/connector"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	githubconn "github.com/hollis-labs/cerberus/internal/connector/github"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/store/sqlite"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
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

// Options configures App construction. ConfigPath is the v2 config file path
// (the legacy ~/.cerberus/config.yaml dotdir location, unchanged). DBPath, when
// non-empty, overrides the go-apppaths-resolved main database path — it is the
// value of the `--db` flag. An empty DBPath uses the XDG-resolved default
// (CERBERUS_DB_PATH is still honored natively by go-apppaths in that case).
type Options struct {
	ConfigPath string
	DBPath     string
}

// New creates an App from a config file path. It loads the unified v2 config,
// creates the connector registry with the local connector, and initializes
// the service registry backed by a file-based config Source.
//
// The SQLite store is NOT opened here — call OpenStore() explicitly when
// needed. This keeps the default path (local services via config) lightweight.
//
// New is a thin convenience wrapper over NewWithOptions for the common case
// of no DB override.
func New(cfgPath string) (*App, error) {
	return NewWithOptions(Options{ConfigPath: cfgPath})
}

// NewWithOptions creates an App with explicit Options. The main database path
// is resolved via go-apppaths (XDG mode); opts.DBPath, when set, overrides it
// through paths.WithDBOverride. go-apppaths additionally honors CERBERUS_DB_PATH
// / CERBERUS_WORKSPACE natively, so an unset --db still respects those env vars.
func NewWithOptions(opts Options) (*App, error) {
	// Assemble the effective v2 config: every registered project config
	// merged over the optional global config.yaml at ConfigPath.
	v2, err := registry.ResolveConfig(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	registry, sec := newConnectorRegistry(opts.ConfigPath)
	local := localconn.New()
	registry.Register(local)

	runtime := cerbapi.NewResourceRuntimeService(AuditSink(),
		cerbapi.WithResourceRuntimeLogger(nil),
		cerbapi.WithResourceRuntimeLocalConnector(local),
		cerbapi.WithResourceRuntimeConfigV2(v2),
		cerbapi.WithResourceRuntimeConfigPath(opts.ConfigPath),
	)
	external := cerbapi.NewExternalConnectorService(AuditSink(), registry)
	external.SetResourceLookup(runtime.ResourceDef)

	// Resolve the main database path via go-apppaths. Only cerberus.db moves
	// onto the XDG layout (CW-20260517-0065); the rest of ~/.cerberus/ stays.
	var layoutOpts []paths.Option
	if opts.DBPath != "" {
		layoutOpts = append(layoutOpts, paths.WithDBOverride(opts.DBPath))
	}
	layout, err := config.ResolveLayout(layoutOpts...)
	if err != nil {
		return nil, fmt.Errorf("resolve store path: %w", err)
	}
	storePath := layout.MainDB()

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

// NewExternalConnectorService builds the in-process admin lane, resolving
// resource ids (SSH targets) against the config at configPath, or the default
// config when it is empty.
func NewExternalConnectorService(configPath ...string) *cerbapi.ExternalConnectorService {
	path := config.DefaultPath()
	if len(configPath) > 0 && configPath[0] != "" {
		path = configPath[0]
	}
	connectors, _ := newConnectorRegistry(path)
	svc := cerbapi.NewExternalConnectorService(AuditSink(), connectors)
	svc.SetResourceLookup(func(id string) (*config.ResourceDef, bool) {
		cfg, err := registry.ResolveConfig(path)
		if err != nil {
			return nil, false
		}
		return cerbapi.ConfigResourceLookup(cfg)(id)
	})
	return svc
}

var (
	auditOnce sync.Once
	auditSink audit.Sink
)

// AuditSink is this process's audit sink: ~/.cerberus/audit, opened once and
// shared, so every service in the process writes through one writer. When the
// directory cannot be opened the sink refuses every write with the reason —
// non-read operations are then refused and reads are logged (Decision 8).
func AuditSink() audit.Sink {
	auditOnce.Do(func() {
		dir, err := AuditDir()
		if err == nil {
			var sink *audit.FileSink
			if sink, err = audit.OpenFileSink(dir); err == nil {
				auditSink = sink
				return
			}
		}
		auditSink = audit.Unavailable{Err: err}
	})
	policyOnce.Do(installPolicy)
	return auditSink
}

var policyOnce sync.Once

// installPolicy installs the decision point every gated operation in this
// process is authorized against: the applied snapshot under
// ~/.cerberus/policy, reloaded when `cerberus policy apply` changes it, and
// the baseline when nothing is applied or the snapshot fails its hash check
// (recorded as policy_snapshot_changed). It is installed with the audit
// sink because every service is built with that sink, so no service can be
// built without it. P2 records its decisions and enforces nothing.
func installPolicy() {
	dir, err := PolicyDir()
	if err != nil {
		return
	}
	cerbapi.SetPolicyDecisionPoint(policy.NewReloading(policy.Store{Dir: dir}, func(status policy.LoadStatus) {
		if err := cerbapi.RecordPolicyLoad(auditSink, status); err != nil {
			slog.Default().Error("policy.load_record_failed", "error", redact.Text(err.Error()))
		}
		if status.Mismatch() {
			slog.Default().Warn("policy.snapshot_changed", "problem", status.Problem, "consequence", "the baseline decides until `cerberus policy apply` runs again")
		}
	}))
}

// ApprovalsDir is ~/.cerberus/approvals.
func ApprovalsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cerberus", "approvals"), nil
}

// PolicyDir is ~/.cerberus/policy.
func PolicyDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cerberus", "policy"), nil
}

// AuditDir is ~/.cerberus/audit.
func AuditDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cerberus", "audit"), nil
}

// NewDaemonConnectorServices builds the daemon's managed plugin lane and its
// admin lane, both writing to this process's audit sink. The admin lane
// resolves resources against the daemon's runtime.
func NewDaemonConnectorServices(a *App, hostVersion string, stderr io.Writer, configPath string) (*cerbapi.ManagedPluginConnectorService, *cerbapi.ExternalConnectorService, error) {
	statePath, err := cerbapi.PluginConnectorStatePath()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve plugin connector state path: %w", err)
	}
	// Plugins resolve their declared credentials through the same provider the
	// built-in connectors use, so `connector-secrets.yaml` and `keychain://`
	// mean the same thing either side of the plugin boundary.
	managed, err := cerbapi.NewManagedPluginConnectorService(AuditSink(), hostVersion, stderr, statePath,
		cerbapi.WithManagedPluginSecrets(a.Secrets),
		cerbapi.WithManagedPluginConnectorConfig(ConnectorConfigPath(configPath)),
		cerbapi.WithManagedPluginReservedIDs(a.Registry.BuiltInIDs()...))
	if err != nil {
		return nil, nil, fmt.Errorf("initialize managed plugin connectors: %w", err)
	}
	external := cerbapi.NewExternalConnectorService(AuditSink(), a.Registry, managed)
	external.SetResourceLookup(a.Runtime.ResourceDef)
	return managed, external, nil
}

// NewResourceRuntimeService builds the in-process supervision lane for the
// CLI, over the config at configPath (the default when empty), writing to
// this process's audit sink. cfg, when non-nil, is an already-resolved config.
func NewResourceRuntimeService(configPath string, cfg *config.ConfigV2) *cerbapi.ResourceRuntimeService {
	opts := []cerbapi.ResourceRuntimeOption{cerbapi.WithResourceRuntimeConfigPath(configPath)}
	if cfg != nil {
		opts = append(opts, cerbapi.WithResourceRuntimeConfigV2(cfg))
	}
	return cerbapi.NewResourceRuntimeService(AuditSink(), opts...)
}

// NewPluginReviewer builds the in-process install review lane over
// ~/.cerberus/plugin-connectors.json and its plugin store, writing to this
// process's audit sink. The built-in connector ids are reserved, as for the
// daemon's managed lane.
func NewPluginReviewer(configPath string) (*cerbapi.PluginReviewer, error) {
	statePath, err := cerbapi.PluginConnectorStatePath()
	if err != nil {
		return nil, fmt.Errorf("resolve plugin connector state path: %w", err)
	}
	registry, _ := newConnectorRegistry(configPath)
	return cerbapi.NewPluginReviewer(AuditSink(), statePath, registry.BuiltInIDs()...), nil
}

// NewPluginConnectorService builds the one-shot plugin lane (`connectors
// plugin exec <dir>`), writing to this process's audit sink.
func NewPluginConnectorService(hostVersion string, stderr io.Writer, configPath string) *cerbapi.PluginConnectorService {
	return cerbapi.NewPluginConnectorService(AuditSink(), hostVersion, stderr,
		cerbapi.WithPluginConnectorSecrets(ConnectorSecrets(configPath)),
		cerbapi.WithPluginConnectorConfig(ConnectorConfigPath(configPath)))
}

func newConnectorRegistry(configPaths ...string) (*connector.Registry, domain.SecretProvider) {
	configPath := config.DefaultPath()
	if len(configPaths) > 0 && configPaths[0] != "" {
		configPath = configPaths[0]
	}
	sec := ConnectorSecrets(configPath)
	registry := connector.NewRegistry()
	registerBuiltInConnectors(registry, sec)
	return registry, sec
}

// ConnectorSecrets builds the credential provider every connector lane
// resolves through: process env, then `connector-secrets.yaml` beside the
// config, then the Cerberus keychain. Exported so the plugin host resolves a
// plugin's declared secrets from the same place a built-in connector does,
// rather than each plugin reimplementing the lookup, and so the web console
// resolves deployment credentials from it too.
//
// Every value it resolves is registered with the request's redaction scope
// (WP-S2), except a value that is not a credential: a secret a built-in
// connector declares with a non-credential Kind, and the non-credential
// values the console keeps beside credentials (nonCredentialSecrets). A
// plugin's secrets are registered by the plugin host, which reads the
// plugin's own manifest.
func ConnectorSecrets(configPaths ...string) domain.SecretProvider {
	configPath := config.DefaultPath()
	if len(configPaths) > 0 && configPaths[0] != "" {
		configPath = configPaths[0]
	}
	provider := secrets.NewReferenceProvider(secrets.NewKeychainProvider(), filepath.Join(filepath.Dir(configPath), "connector-secrets.yaml"))
	return secrets.Registering(provider, notACredential)
}

// nonCredentialSecrets are values read through the secret chain that are
// names, not credentials. The Vercel deploy falls back to vercel/scope, a
// team slug the console's provider catalog declares as a field, and the
// deploy prints it; registering it would cut the team's name out of the
// deploy's own output.
var nonCredentialSecrets = map[string]bool{"vercel/scope": true}

// notACredential reports the values ConnectorSecrets resolves without
// registering them for value redaction.
func notACredential(service, key string) bool {
	return builtInNonCredential(service, key) || nonCredentialSecrets[service+"/"+key]
}

// builtInNonCredentials is, per built-in connector id, the secrets its
// definition declares as something other than a credential
// (contract.SecretRequirement.Kind). It is read from the definitions, not
// listed here, so a connector that declares a new one is exempt without this
// file changing.
var builtInNonCredentials = func() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, def := range []contract.Definition{dockerconn.Definition(), githubconn.Definition(), sshconn.Definition()} {
		for _, req := range def.Config.Secrets {
			if !req.IsCredential() {
				if out[def.ID] == nil {
					out[def.ID] = map[string]bool{}
				}
				out[def.ID][req.Name] = true
			}
		}
	}
	return out
}()

// builtInNonCredential reports whether service/key is a built-in secret
// declared as a non-credential. A built-in connector resolves under its id,
// or "<id>/<resource-id>" for a secret held per resource
// (ssh/<resource-id>/key).
func builtInNonCredential(service, key string) bool {
	id, _, _ := strings.Cut(service, "/")
	return builtInNonCredentials[id][key]
}

// ConnectorConfigPath is connector-config.yaml beside the global config: the
// operator-owned file of plugin fields and MCP exposure. Cerberus reads it and
// never writes it.
func ConnectorConfigPath(configPaths ...string) string {
	configPath := config.DefaultPath()
	if len(configPaths) > 0 && configPaths[0] != "" {
		configPath = configPaths[0]
	}
	return filepath.Join(filepath.Dir(configPath), pluginhost.ConnectorConfigFilename)
}

func registerBuiltInConnectors(registry *connector.Registry, sec domain.SecretProvider) {
	registry.RegisterDefinition(dockerconn.Definition())
	registry.RegisterDefinition(githubconn.Definition())
	registry.RegisterDefinition(sshconn.Definition())

	registry.RegisterFactory(githubconn.Definition(), func(ctx context.Context) (contract.Connector, error) {
		return githubconn.New(secrets.WithContext(ctx, sec))
	})
	// Docker resolves per call, like the credentialed connectors above. Eager
	// registration cached a boot-time "docker CLI not found" for the daemon's
	// whole lifetime, so starting Docker Desktop — or correcting the daemon's
	// PATH — could not recover without a restart.
	registry.RegisterFactory(dockerconn.Definition(), func(context.Context) (contract.Connector, error) {
		return dockerconn.New()
	})

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
