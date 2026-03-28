package domain

import "context"

// SecretProvider retrieves and stores secrets (API keys, tokens, etc.).
// The default implementation uses the OS keychain with env var fallback.
type SecretProvider interface {
	Get(ctx context.Context, service, key string) (string, error)
	Set(ctx context.Context, service, key, value string) error
	Delete(ctx context.Context, service, key string) error
}
