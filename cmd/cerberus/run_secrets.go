package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secretref"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/settings"
)

// runSecretsTimeout bounds the whole resolution pass. A keyring read can block on a prompt; a
// managed service should fail loudly rather than hang in its supervisor's start state forever.
const runSecretsTimeout = 30 * time.Second

// runSecretsCmd is the exec-time half of Cerberus's secret handling (the old build's
// cmd_run_secrets.go).
//
// Cerberus renders a managed service's environment into its systemd unit or launchd plist, so a
// literal credential in a project file would end up in plaintext there as well. Writing a
// secretref reference instead keeps both files clean, and this shim turns the reference back
// into a credential, in the service's own process, at the moment it starts, never on disk.
//
// A unit for a service whose environment carries references runs:
//
//	cerberus run-secrets -- <program> [args...]
//
// The shim resolves every reference in its own environment and then replaces itself with the
// target via execve, so the supervisor supervises the real process: PID, exit status and
// restarts behave exactly as they do without it.
//
// It needs neither the server nor the database: it is not in needsData, so only the modules
// that read config start. Keyring references touch nothing else. A vault reference (op://,
// keeper://) loads the plugin that claims its scheme here, recorded on the audit chain in
// <data>/audit, and stops it before exec.
func (c *cerberus) runSecretsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run-secrets -- <program> [args...]",
		Short: "Resolve secret references in the environment, then exec a program",
		Long: "Resolve secretref references (keychain://…, helper://…, op://…) found in this\n" +
			"process's environment and exec the given program with the resolved values.\n\n" +
			"This is how Cerberus keeps credentials out of generated service units:\n" +
			"the unit carries references, and resolution happens in the service's own\n" +
			"process at start time. Values that are not references are passed through\n" +
			"untouched.\n\n" +
			"Resolved secrets are never printed. A reference that cannot be resolved is\n" +
			"a fatal error — the program is not started with a blank credential.",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		// One line in the service's stderr log (main prints it), redacted as old's was.
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			backends := c.serviceSecretBackends(os.Getenv, os.Stderr)
			if err := runSecrets(cmd.Context(), args, secrets.NewKeychainProvider(), backends, syscall.Exec); err != nil {
				return errors.New(redact.ErrorText(err))
			}
			return nil
		},
	}
}

// secretBackends are run-secrets' vault router, stopped before exec.
type secretBackends interface {
	secretref.SchemeRouter
	Close(context.Context)
}

// execFunc is syscall.Exec; tests replace it.
type execFunc func(argv0 string, argv, envv []string) error

func runSecrets(ctx context.Context, args []string, keyring secretref.Provider, backends secretBackends, execve execFunc) error {
	if len(args) == 0 {
		return errors.New("run-secrets requires a program to exec")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, runSecretsTimeout)
	defer cancel()

	// Vault references (op://, keeper://) resolve through the installed secret-backend plugins,
	// loaded here, in the service's own process, and stopped before exec. The server is not
	// asked: a socket call that returned values would be an oracle for any same-user process,
	// and a service must start while the server is down.
	defer backends.Close(context.Background())
	resolver := secretref.NewResolver(keyring, secretref.WithSchemeRouter(backends))

	resolved, err := resolver.ResolveEnv(ctx, environMap())
	if err != nil {
		return fmt.Errorf("resolve service environment: %w", err)
	}

	program, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("locate %s: %w", args[0], err)
	}

	// No backend outlives resolution: execve would orphan it, still holding the service's
	// resolved secrets in its memory.
	backends.Close(ctx)

	// execve replaces this process so the supervisor supervises the target directly rather
	// than this shim. On success it does not return.
	if err := execve(program, args, environSlice(resolved)); err != nil {
		return fmt.Errorf("exec %s: %w", program, err)
	}
	return nil
}

// environMap reads the current environment into a map. A variable whose value contains "="
// keeps it; only the first separator splits name from value.
func environMap() map[string]string {
	entries := os.Environ()
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		out[name] = value
	}
	return out
}

// environSlice renders an environment map back into execve form, sorted so the child's
// environment is deterministic.
func environSlice(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+env[name])
	}
	return out
}

// serviceSecretBackends is the vault router for run-secrets: the secret-backend plugins, loaded
// in the managed service's own process rather than asked of the server. It is built at the
// first vault reference, so a service whose environment names none reads no plugin state,
// opens no audit chain and writes nothing.
//
// Everything it needs comes without the database: the settings (cerberus.adopt_root,
// cerberus.connectors) from the config module the CLI always starts; plugin-connectors.json and
// the plugin store from the adopted root (admin.Paths); connector-secrets.yaml from the config
// dir; the applied policy (for the posture a changed bundle is judged under) from the policy
// directory, read only; and the audit chain in <data>/audit, which must already exist. Each
// backend load is recorded there through a gate with no broker, as automation via run_secrets.
func (c *cerberus) serviceSecretBackends(getenv func(string) string, stderr io.Writer) *lazyBackends {
	return &lazyBackends{build: func() (*admin.ProcessBackends, error) {
		dirs, err := datadir.Resolve(getenv)
		if err != nil {
			return nil, err
		}
		sink, err := openRunSecretsAudit(filepath.Join(dirs.Data, "audit"))
		if err != nil {
			return nil, err
		}
		st, err := settings.Parse(c.app.Config[settings.Key], c.home)
		if err != nil {
			return nil, err
		}
		pdp, _ := policy.Store{Dir: policyDir(c.home), AuditDir: sink.Dir()}.LoadVerified()
		shim, _ := pluginhost.ExecutablePath()
		return admin.NewProcessBackends(admin.ProcessBackendsOptions{
			Gate:            gate.New(gate.Options{Sink: sink, Policy: pdp, Logger: slog.Default()}),
			Paths:           admin.Paths{AdoptRoot: st.AdoptRoot, ConfigDir: configDir(c.home)},
			ConnectorConfig: st.Connectors,
			HostVersion:     buildVersion(),
			Shim:            shim,
			Stderr:          stderr,
			Logger:          slog.Default(),
		})
	}}
}

// openRunSecretsAudit opens the existing audit chain in dir. A chain that is not there yet is
// not created: its digest key must be the one the migration adopted, so the server, not a
// service's start, creates it. Without the chain no backend is loaded (fail closed).
func openRunSecretsAudit(dir string) (*audit.FileSink, error) {
	if _, err := audit.OpenExistingDigester(dir); err != nil {
		if errors.Is(err, audit.ErrNoDigestKey) {
			return nil, fmt.Errorf("the audit chain in %s does not exist yet, and a secret backend is not started unrecorded; start Cerberus once so it creates the chain, then restart this service", dir)
		}
		return nil, fmt.Errorf("the audit chain in %s cannot be read, and a secret backend is not started unrecorded: %w", dir, err)
	}
	sink, err := auditchain.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open the audit chain in %s, and a secret backend is not started unrecorded: %w", dir, err)
	}
	return sink, nil
}

// lazyBackends builds its ProcessBackends on first use.
type lazyBackends struct {
	build func() (*admin.ProcessBackends, error)
	once  sync.Once
	b     *admin.ProcessBackends
	err   error
}

var _ secretBackends = (*lazyBackends)(nil)

func (l *lazyBackends) get() (*admin.ProcessBackends, error) {
	l.once.Do(func() { l.b, l.err = l.build() })
	return l.b, l.err
}

// Claims reports whether an installed plugin claims scheme.
func (l *lazyBackends) Claims(scheme string) bool {
	b, err := l.get()
	return err == nil && b.Claims(scheme)
}

// ResolveSecret resolves ref through the plugin that claims its scheme.
func (l *lazyBackends) ResolveSecret(ctx context.Context, ref string) (string, error) {
	b, err := l.get()
	if err != nil {
		scheme, _, _ := strings.Cut(ref, "://")
		return "", fmt.Errorf("credential_missing: %s:// reference: the installed secret backends could not be read (%w)", scheme, err)
	}
	return b.ResolveSecret(ctx, ref)
}

// Backend names the plugin that resolves scheme, as id@version.
func (l *lazyBackends) Backend(scheme string) string {
	if b, err := l.get(); err == nil {
		return b.Backend(scheme)
	}
	return ""
}

// Close stops every backend loaded, if any were.
func (l *lazyBackends) Close(ctx context.Context) {
	if l.b != nil {
		l.b.Close(ctx)
	}
}
