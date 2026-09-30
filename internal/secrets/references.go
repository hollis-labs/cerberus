package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/secretref"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

// ReferenceProvider reads connector-to-reference mappings on each lookup.
// Precedence: explicit process environment, reference mapping, keychain. It
// only resolves: the mapping file holds references, never values, and writing
// a keychain entry is done against the keychain itself, not through here.
type ReferenceProvider struct {
	base     secret.Reader
	path     string
	resolver *secretref.Resolver
}

// NewReferenceProvider resolves through base and the mapping at path. opts
// configure the reference resolver: WithSchemeRouter to reach secret-backend
// plugins, or WithoutSchemeRouter for the core chain.
func NewReferenceProvider(base secret.Reader, path string, opts ...secretref.Option) *ReferenceProvider {
	return &ReferenceProvider{base: base, path: path, resolver: secretref.NewResolver(base, opts...)}
}

func (p *ReferenceProvider) Get(ctx context.Context, service, key string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	file, err := p.Bindings()
	if err != nil {
		return "", err
	}
	// A per-access binding (I9) decides for its key, ahead of the legacy
	// chain: an environment value cannot put a write credential under a
	// read binding.
	sources, name := SourcesFrom(ctx), service+"/"+key
	var b Binding
	if scope, ok := CredentialScopeFrom(ctx); ok {
		b = file[service].Resolve(service, key, &scope)
		if b.None {
			sources.record(name, "binding:"+b.Label+", none")
			return "", &NoCredentialError{Connector: service, Key: key, Access: scope.Access, Target: scope.Target.ID, Label: b.Label}
		}
	} else {
		b = file[service].Resolve(service, key, nil)
	}
	value, where := b.Ref, "binding:"+b.Label
	if b.Label == "" {
		// The legacy chain: the environment, the flat key, the keychain.
		where = "mapping"
		if env := os.Getenv(envVarName(service, key)); env != "" {
			value, where = env, "env"
		}
		if value == "" && p.base != nil {
			where = "keyring"
			if value, err = p.base.Get(ctx, service, key); err != nil {
				sources.record(name, where+", unresolved")
				return "", err
			}
		}
	}
	if value == "" {
		sources.record(name, "missing")
		return "", nil
	}
	if p.resolver.IsRef(value) {
		source := p.sourceOf(where, value)
		resolved, err := p.resolver.Resolve(ctx, value)
		if err != nil {
			source += ", unresolved"
		}
		sources.record(name, source)
		return resolved, err
	}
	sources.record(name, where)
	return value, nil
}

// sourceOf names where a reference led: where it was found, its scheme, and
// for a vault, the plugin that resolves it ("mapping:op via
// onepassword@0.1.0"). Names only; never the reference's path.
func (p *ReferenceProvider) sourceOf(where, ref string) string {
	scheme, _, _ := strings.Cut(ref, "://")
	source := where + ":" + scheme
	if backend := p.resolver.Backend(scheme); backend != "" {
		source += " via " + backend
	}
	return source
}

// Bindings is the parsed mapping file; an absent file binds nothing.
func (p *ReferenceProvider) Bindings() (BindingFile, error) {
	if p.path == "" {
		return BindingFile{}, nil
	}
	data, err := os.ReadFile(p.path) //nolint:gosec // operator-owned connector reference mapping
	if os.IsNotExist(err) {
		return BindingFile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read connector secret references: %w", err)
	}
	return ParseBindings(data, p.path, p.resolver.IsRef)
}

// NoCredentialError is a call whose binding says there is no credential
// for its access: refused, never retried with the other access.
type NoCredentialError struct {
	Connector, Key string
	Access         Access
	Target         string
	Label          string
}

func (e *NoCredentialError) Error() string {
	on := ""
	if e.Target != "" {
		on = " on " + e.Target
	}
	return fmt.Sprintf("%s %ss%s have no %s credential (connector-secrets.yaml %s sets %s to null), so nothing ran; the %s credential is never used instead. Bind a %s credential there if this should be allowed",
		e.Connector, e.Access, on, e.Access, e.Label, e.Key, otherAccess(e.Access), e.Access)
}

// Unwrap is ErrNoCredential.
func (e *NoCredentialError) Unwrap() error { return ErrNoCredential }

func otherAccess(a Access) string {
	if a == AccessWrite {
		return "read"
	}
	return "write"
}

type contextualProvider struct {
	secret.Reader
	ctx context.Context
}

func (p contextualProvider) Get(_ context.Context, service, key string) (string, error) {
	return p.Reader.Get(p.ctx, service, key)
}

// WithContext carries the operation deadline through legacy constructors that
// request credentials with context.Background().
func WithContext(ctx context.Context, provider secret.Reader) secret.Reader {
	if provider == nil {
		return nil
	}
	return contextualProvider{Reader: provider, ctx: ctx}
}
