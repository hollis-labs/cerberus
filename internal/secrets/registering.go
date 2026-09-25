package secrets

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/redact"
	secret "github.com/hollis-labs/cerberus/pkg/secret"
)

// registeringProvider registers every value it resolves with the request's
// redaction scope, at the point of resolution, where the credential is still
// a value. Any error or log text the request renders through the scope then
// loses it, with a label or without one, whoever composed the message.
type registeringProvider struct {
	secret.Provider
	isPath func(service, key string) bool
}

// Registering wraps provider so that each value Get resolves is added to
// redact.ScopeFrom(ctx), named service/key. A value isPath reports as a file
// path is not registered: a path is guidance, not a credential, and value
// redaction would cut it out of the error that needs to show it. A ctx with
// no scope registers nothing, and the value is returned either way.
func Registering(provider secret.Provider, isPath func(service, key string) bool) secret.Provider {
	if provider == nil {
		return nil
	}
	return registeringProvider{Provider: provider, isPath: isPath}
}

func (p registeringProvider) Get(ctx context.Context, service, key string) (string, error) {
	value, err := p.Provider.Get(ctx, service, key)
	if err == nil && value != "" && (p.isPath == nil || !p.isPath(service, key)) {
		redact.ScopeFrom(ctx).Add(service+"/"+key, value)
	}
	return value, err
}
