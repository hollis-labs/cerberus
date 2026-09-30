package secrets

import (
	"context"
	"fmt"
	"os"
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
	var b Binding
	if scope, ok := CredentialScopeFrom(ctx); ok {
		b = file[service].Resolve(service, key, &scope)
		if b.None {
			return "", &NoCredentialError{Connector: service, Key: key, Access: scope.Access, Target: scope.Target.ID, Label: b.Label}
		}
	} else {
		b = file[service].Resolve(service, key, nil)
	}
	value := b.Ref
	if b.Label == "" {
		// The legacy chain: the environment, the flat key, the keychain.
		if env := os.Getenv(envVarName(service, key)); env != "" {
			value = env
		}
		if value == "" && p.base != nil {
			if value, err = p.base.Get(ctx, service, key); err != nil {
				return "", err
			}
		}
	}
	if p.resolver.IsRef(value) {
		return p.resolver.Resolve(ctx, value)
	}
	return value, nil
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
