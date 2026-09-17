package domain

import "github.com/hollis-labs/cerberus/pkg/secret"

// SecretProvider retrieves and stores secrets (API keys, tokens, etc.).
// The default implementation uses the OS keychain with env var fallback.
type SecretProvider = secret.Provider
