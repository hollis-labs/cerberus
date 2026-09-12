package secrets

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chrispian/cerberus/internal/secretref"
	"github.com/chrispian/cerberus/pkg/secret"
	"gopkg.in/yaml.v3"
)

// ReferenceProvider reads connector-to-reference mappings on each lookup. The
// underlying keychain remains the writer; this file contains references only.
// Precedence: explicit process environment, reference mapping, keychain.
type ReferenceProvider struct {
	secret.Provider
	path     string
	resolver *secretref.Resolver
}

func NewReferenceProvider(base secret.Provider, path string) *ReferenceProvider {
	return &ReferenceProvider{Provider: base, path: path, resolver: secretref.NewResolver(base)}
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
					if !secretref.IsRef(ref) {
						return "", fmt.Errorf("connector secret %s/%s must be a keychain:// or helper:// reference; literal credentials are not allowed in %s", connector, name, p.path)
					}
				}
			}
			value = refs[service][key]
		}
	}
	if value == "" && p.Provider != nil {
		var err error
		value, err = p.Provider.Get(ctx, service, key)
		if err != nil {
			return "", err
		}
	}
	if secretref.IsRef(value) {
		return p.resolver.Resolve(ctx, value)
	}
	return value, nil
}

type contextualProvider struct {
	secret.Provider
	ctx context.Context
}

func (p contextualProvider) Get(_ context.Context, service, key string) (string, error) {
	return p.Provider.Get(p.ctx, service, key)
}

// WithContext carries the operation deadline through legacy constructors that
// request credentials with context.Background().
func WithContext(ctx context.Context, provider secret.Provider) secret.Provider {
	if provider == nil {
		return nil
	}
	return contextualProvider{Provider: provider, ctx: ctx}
}
