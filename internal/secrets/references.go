package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/secretref"
	"github.com/hollis-labs/cerberus/pkg/secret"
	"gopkg.in/yaml.v3"
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
	value := os.Getenv(envVarName(service, key))
	if value == "" && p.path != "" {
		data, err := os.ReadFile(p.path) //nolint:gosec // operator-owned connector reference mapping
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("read connector secret references: %w", err)
		}
		if err == nil {
			var refs map[string]map[string]string
			if yaml.Unmarshal(data, &refs) != nil {
				return "", fmt.Errorf("invalid connector secret reference mapping in %s; expected connector -> key -> reference", p.path)
			}
			for connector, keys := range refs {
				for name, ref := range keys {
					if !p.resolver.IsRef(ref) {
						return "", fmt.Errorf("connector secret %s/%s must be a secret reference (%s://); literal credentials are not allowed in %s",
							connector, name, strings.Join(secretref.Schemes(), "://, "), p.path)
					}
				}
			}
			value = refs[service][key]
		}
	}
	if value == "" && p.base != nil {
		var err error
		value, err = p.base.Get(ctx, service, key)
		if err != nil {
			return "", err
		}
	}
	if p.resolver.IsRef(value) {
		return p.resolver.Resolve(ctx, value)
	}
	return value, nil
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
