package secret

import "context"

// Provider retrieves and stores connector secrets such as API keys and tokens.
type Provider interface {
	Get(ctx context.Context, service, key string) (string, error)
	Set(ctx context.Context, service, key, value string) error
	Delete(ctx context.Context, service, key string) error
}
