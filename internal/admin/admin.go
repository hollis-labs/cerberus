// Package admin is the admin lane (docs/substrate-port-plan.md section 3, slice 7): the
// built-in ssh and docker connectors and the external connector service over them, the managed
// plugins on the adopted plugin roots (pkg/plugin unchanged), the credential chain and store
// (keyring service and account names unchanged), and the credential broker, its receipts in the
// class A store. It is the old build's admin lane (internal/cerbapi on main:
// external_connector_service.go, managed_plugin_connector_service.go and their helpers), with
// every write through the operation gate (internal/gate). It exists in supervise mode only:
// observe mode starts no plugin and writes nothing.
package admin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/connector"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/credentialbroker"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/secretref"
	"github.com/hollis-labs/cerberus/internal/secrets"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

// Paths are where the admin lane's files live. Everything the old build kept beside its config
// that other programs or recorded absolute paths point into stays in the adopted root, read and
// written in place (plan section 3); the operator's reference mapping is the config dir's.
type Paths struct {
	// AdoptRoot is cerberus.adopt_root (~/.cerberus): plugins/, plugin-data/, plugin-cache/,
	// plugin-connectors.json, credential-issuance/ and transfers/.
	AdoptRoot string
	// ConfigDir is <XDG_CONFIG_HOME>/cerberus, beside the override file: connector-secrets.yaml
	// (copied there at 0600 by the migration, plan section 5).
	ConfigDir string
}

// PluginState is plugin-connectors.json, shared with the old build in its format.
func (p Paths) PluginState() string { return filepath.Join(p.AdoptRoot, "plugin-connectors.json") }

// PluginStore is plugins/, the reviewed bundles (<id>/<digest>/).
func (p Paths) PluginStore() string { return filepath.Join(p.AdoptRoot, "plugins") }

// PluginData is plugin-data/, each plugin's private data directory.
func (p Paths) PluginData() string { return filepath.Join(p.AdoptRoot, "plugin-data") }

// PluginCache is plugin-cache/, each plugin's cache directory.
func (p Paths) PluginCache() string { return filepath.Join(p.AdoptRoot, "plugin-cache") }

// Issuance is credential-issuance/: the issued secrets under secrets/issued/<id>/<name>.
func (p Paths) Issuance() string { return filepath.Join(p.AdoptRoot, "credential-issuance") }

// Transfers is transfers/, the root a non-terminal caller's local paths are held to.
func (p Paths) Transfers() string { return filepath.Join(p.AdoptRoot, "transfers") }

// PolicyDir is the operator's policy directory, beside the override file: a reviewed plugin's
// suggested rules are written there as a working file (providers/<id>.yaml), inert until applied.
func (p Paths) PolicyDir() string {
	if p.ConfigDir == "" {
		return ""
	}
	return filepath.Join(p.ConfigDir, "policy")
}

// ConnectorSecrets is connector-secrets.yaml, the reference mapping and per-access bindings.
func (p Paths) ConnectorSecrets() string {
	if p.ConfigDir == "" {
		return ""
	}
	return filepath.Join(p.ConfigDir, "connector-secrets.yaml")
}

// Options build the lane.
type Options struct {
	// Gate is the operation gate; required (the lane exists in supervise mode only).
	Gate  *gate.Gate
	Paths Paths
	// Resources looks a configured resource up, read fresh (the registry's).
	Resources gate.ResourceLookup
	// ConnectorConfig is the plugins' fields and MCP exposure: cerberus.connectors, the old
	// connector-config.yaml.
	ConnectorConfig pluginhost.ConnectorConfig
	// Receipts keeps the credential broker's receipts; nil keeps them as files beside the
	// issued secrets, as the old build did.
	Receipts credentialbroker.Recorder
	// HostVersion is this build's version, for plugin host ranges.
	HostVersion string
	// Shim is the cerberus binary whose __plugin-exec sets a plugin's rlimits; empty launches
	// plugins directly.
	Shim string
	// Stderr receives plugin stderr and the lane's warnings.
	Stderr io.Writer
	Logger *slog.Logger
	// Keyring is the OS credential store: the last link of the chain, and what the console's
	// provider save writes. Nil is the Cerberus keychain (service "cerberus", account
	// "<id>/<name>").
	Keyring secret.ReadWriter
	// SSHBackend replaces the ssh connector's transport (tests); nil is the real one.
	SSHBackend sshconn.BackendFactory
	// Docker replaces the docker connector's construction (tests); nil is the docker CLI.
	Docker func(context.Context) (contract.Connector, error)
}

// Service is the admin lane.
type Service struct {
	o        Options
	gate     *gate.Gate
	registry *connector.Registry
	// chain resolves every connector's credentials: the environment, connector-secrets.yaml,
	// a secret-backend plugin for a vault reference, then the keyring. It registers each
	// credential it resolves with the request's redaction scope.
	chain secret.Reader
	// refs is the chain's reference provider, for presence and the bindings.
	refs *secrets.ReferenceProvider
	// core is the chain without a vault: a secret backend's own credentials.
	core     secret.Reader
	backends *secrets.BackendRouter
	keyring  secret.ReadWriter
	plugins  *Plugins
	logger   *slog.Logger
}

// New builds the lane: the built-in connectors, the credential chain, and the managed plugins
// registered from plugin-connectors.json with none loaded (RestorePlugins loads them).
func New(o Options) (*Service, error) {
	if o.Gate == nil {
		return nil, errors.New("admin: the operation gate is required; the admin lane runs in supervise mode only")
	}
	if o.Paths.AdoptRoot == "" || !filepath.IsAbs(o.Paths.AdoptRoot) {
		return nil, errors.New("admin: the adopted root must be an absolute path")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Keyring == nil {
		o.Keyring = secrets.NewKeychainProvider()
	}
	s := &Service{o: o, gate: o.Gate, keyring: o.Keyring, backends: &secrets.BackendRouter{}, logger: o.Logger}
	s.refs = secrets.NewReferenceProvider(o.Keyring, o.Paths.ConnectorSecrets(), secretref.WithSchemeRouter(s.backends))
	s.chain = secrets.Registering(s.refs, notACredential)
	s.core = secrets.Registering(secrets.NewReferenceProvider(o.Keyring, o.Paths.ConnectorSecrets(), secretref.WithoutSchemeRouter(coreSecretsOnly)), notACredential)
	s.registry = connector.NewRegistry()
	s.registerBuiltIns()
	plugins, err := newPlugins(s)
	if err != nil {
		return nil, err
	}
	s.plugins = plugins
	return s, nil
}

// Plugins is the managed plugin lane.
func (s *Service) Plugins() *Plugins { return s.plugins }

// Gate is the operation gate the lane writes through.
func (s *Service) Gate() *gate.Gate { return s.gate }

// coreSecretsOnly explains a vault reference in the core chain.
const coreSecretsOnly = "a secret backend's own credential must come from the OS credential store (keyring://) or the environment, never from another vault"

// registerBuiltIns is the old registerBuiltInConnectors: docker resolved per call (a missing
// docker CLI at start is not cached for the process's life), ssh over the credential chain.
func (s *Service) registerBuiltIns() {
	s.registry.RegisterDefinition(dockerconn.Definition())
	s.registry.RegisterDefinition(sshconn.Definition())
	docker := s.o.Docker
	if docker == nil {
		docker = func(context.Context) (contract.Connector, error) { return dockerconn.New() }
	}
	s.registry.RegisterFactory(dockerconn.Definition(), docker)
	s.registry.Register(sshconn.NewWithBackendFactory(s.chain, s.o.SSHBackend))
}

// SSH is the ssh connector, resolved per call, for the host monitor's probes.
func (s *Service) SSH(ctx context.Context) (*sshconn.Connector, error) {
	c, err := s.registry.Resolve(ctx, "ssh")
	if err != nil {
		return nil, err
	}
	ssh, ok := c.(*sshconn.Connector)
	if !ok {
		return nil, errors.New("the ssh connector cannot run a probe")
	}
	return ssh, nil
}

// lookup is the lane's resource lookup, or nil.
func (s *Service) lookup() func(string) (*config.ResourceDef, bool) {
	if s.o.Resources == nil {
		return nil
	}
	return s.o.Resources
}

// bindings is connector-secrets.yaml's per-access bindings, or none.
func (s *Service) bindings() secrets.BindingFile {
	file, err := s.refs.Bindings()
	if err != nil {
		return secrets.BindingFile{}
	}
	return file
}

// notACredential reports the values the chain resolves without registering them for value
// redaction: a built-in secret declared as something other than a credential.
func notACredential(service, key string) bool {
	id, _, _ := strings.Cut(service, "/")
	return builtInNonCredentials[id][key]
}

var builtInNonCredentials = func() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, def := range []contract.Definition{dockerconn.Definition(), sshconn.Definition()} {
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
