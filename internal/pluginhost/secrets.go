package pluginhost

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

// SecretResolver is the read half of the host's secret provider, as the plugin
// host needs it. The host resolves a plugin's declared credentials on its
// behalf and hands them over the Init config channel; the plugin never reaches
// the store itself.
//
// It is deliberately narrower than secret.Provider: resolving for a plugin is
// a host decision, and nothing about the plugin lane should be able to write
// to or delete from the operator's credential store.
type SecretResolver interface {
	Get(ctx context.Context, service, key string) (string, error)
}

// resolvedSecrets is what Load hands to Init, plus what it could not fill.
// Only Config ever carries a value; everything else in here is names and
// redacted diagnostics, because these fields reach logs.
type resolvedSecrets struct {
	Config map[string]string

	// MissingRequired names declared-required secrets that resolved empty.
	MissingRequired []string

	// Problems describes lookups that errored, redacted. A store that refuses
	// to unlock is worth reporting; its error text is not worth trusting.
	Problems []string
}

// minRedactedValueLength is the shortest resolved value the plugin redactor
// will look for. The redactor matches anywhere in the text, so a short or
// common value — a region, an IP octet, "true" — would be cut out of every
// sentence that happens to contain it, recovery instructions included. A
// credential worth protecting this way is longer than this.
const minRedactedValueLength = 8

// redactor removes every resolved value from text, in the raw form and in
// the escaped forms a value takes when a plugin puts it in a URL — which is
// where a transport error echoes it back. It also names, never shows, the
// secrets too short to redact safely, so the caller can say they are not
// covered.
func (r resolvedSecrets) redactor() (redact.Redactor, []string) {
	names := make([]string, 0, len(r.Config))
	for name := range r.Config {
		names = append(names, name)
	}
	sort.Strings(names)

	var values, unprotected []string
	for _, name := range names {
		value := r.Config[name]
		if len(value) < minRedactedValueLength {
			unprotected = append(unprotected, name)
			continue
		}
		values = append(values, value)
		for _, escaped := range []string{url.QueryEscape(value), url.PathEscape(value)} {
			if escaped != value && !slices.Contains(values, escaped) {
				values = append(values, escaped)
			}
		}
	}
	return redact.New(values...), unprotected
}

// resolvePluginSecrets resolves exactly the secrets a plugin's own manifest
// declares, keyed in the Init config map by the manifest secret name. A plugin
// that declares `token` reads `config["token"]`; it is never handed a secret
// belonging to another connector, and never the store.
//
// Each secret is looked up as <plugin id>/<secret name> through the same
// provider the built-in connectors use, so `CERBERUS_<ID>_<NAME>`,
// `connector-secrets.yaml` and `keychain://` behave identically either side of
// the plugin boundary.
//
// Nothing here is fatal. docs/secrets.md promises that a missing credential
// fails an operation, not a load — and an optional component that can take the
// host down is the failure mode the managed-plugin restore bug already taught
// us to avoid.
func resolvePluginSecrets(ctx context.Context, resolver SecretResolver, plugin InstalledPlugin) resolvedSecrets {
	out := resolvedSecrets{Config: make(map[string]string)}
	for _, req := range plugin.Manifest.Config.Secrets {
		if req.Name == "" {
			continue
		}
		var (
			value string
			err   error
		)
		if resolver != nil {
			value, err = resolver.Get(ctx, plugin.ID, req.Name)
		}
		if err != nil {
			out.Problems = append(out.Problems, fmt.Sprintf("%s: %s", req.Name, redact.Text(err.Error())))
		}
		if value == "" {
			if req.Required {
				out.MissingRequired = append(out.MissingRequired, req.Name)
			}
			continue
		}
		out.Config[req.Name] = value
	}
	sort.Strings(out.MissingRequired)
	return out
}

// MissingSecretsError reports an operation that failed on a plugin which
// loaded without a credential its own manifest declares as required.
//
// The host cannot tell which operation needs which secret — the manifest maps
// secrets to the connector, not to an operation — so this wraps a real failure
// rather than pre-empting the call. ContextForge is the case that matters:
// `get_health` is open and must keep working as the way to tell a down tunnel
// from a down gateway, while `list_gateways` 401s and deserves to say why.
type MissingSecretsError struct {
	Connector string
	Secrets   []string
	Err       error
}

func (e *MissingSecretsError) Error() string {
	envVars := make([]string, 0, len(e.Secrets))
	for _, name := range e.Secrets {
		envVars = append(envVars, secrets.EnvVarName(e.Connector, name))
	}
	// Worded to survive redact.Text, which runs over every operator-facing
	// error. "credential token: set FOO" reads as an assignment to a key named
	// "token" and comes out "credential token: [REDACTED] FOO" — the safety net
	// eating the instruction it was protecting. An em dash is not an assignment
	// separator, so the guidance arrives intact.
	msg := fmt.Sprintf(
		"plugin %q loaded without the required credential %s — supply it through %s, or add a keychain:// reference under %q in ~/.cerberus/connector-secrets.yaml (see docs/secrets.md), then reload it with `cerberus connectors plugin managed load %s`",
		e.Connector,
		strings.Join(e.Secrets, ", "),
		strings.Join(envVars, " or "),
		e.Connector,
		e.Connector,
	)
	if e.Err == nil {
		return msg
	}
	return redact.Text(e.Err.Error()) + "; " + msg
}

func (e *MissingSecretsError) Unwrap() error {
	return e.Err
}
