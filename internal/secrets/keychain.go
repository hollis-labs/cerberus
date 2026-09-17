package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

// KeychainProvider implements domain.SecretProvider using OS keychain
// with environment variable fallback.
type KeychainProvider struct {
	serviceName string
}

// NewKeychainProvider returns a KeychainProvider with the default service name "cerberus".
func NewKeychainProvider() *KeychainProvider {
	return &KeychainProvider{serviceName: "cerberus"}
}

// NewKeychainProviderWithService returns a KeychainProvider with a custom service name.
func NewKeychainProviderWithService(serviceName string) *KeychainProvider {
	return &KeychainProvider{serviceName: serviceName}
}

// Get retrieves a secret using a fallback chain:
// 1. Environment variable CERBERUS_<SERVICE>_<KEY> (uppercase, hyphens to underscores)
// 2. OS keychain via go-keyring
// 3. Returns empty string and nil error if not found
func (k *KeychainProvider) Get(_ context.Context, service, key string) (string, error) {
	// Check environment variable first.
	envKey := envVarName(service, key)
	if val := os.Getenv(envKey); val != "" {
		return val, nil
	}

	// Check OS keychain.
	user := service + "/" + key
	secret, err := keyring.Get(k.serviceName, user)
	if err != nil {
		if err == keyring.ErrNotFound { //nolint:errorlint
			return "", nil
		}
		return "", fmt.Errorf("keychain get %s/%s: %w", service, key, err)
	}
	return secret, nil
}

// Set stores a secret in the OS keychain.
func (k *KeychainProvider) Set(_ context.Context, service, key, value string) error {
	user := service + "/" + key
	if err := keyring.Set(k.serviceName, user, value); err != nil {
		return fmt.Errorf("keychain set %s/%s: %w", service, key, err)
	}
	return nil
}

// Delete removes a secret from the OS keychain.
func (k *KeychainProvider) Delete(_ context.Context, service, key string) error {
	user := service + "/" + key
	if err := keyring.Delete(k.serviceName, user); err != nil {
		if err == keyring.ErrNotFound { //nolint:errorlint
			return nil
		}
		return fmt.Errorf("keychain delete %s/%s: %w", service, key, err)
	}
	return nil
}

// EnvVarName builds the environment variable name a connector credential can
// be supplied through: CERBERUS_<SERVICE>_<KEY>, uppercased with hyphens
// replaced by underscores. It is exported so operator-facing "how do I supply
// this credential" messages name the same variable the provider actually
// reads, rather than a second formatter that can drift from it.
func EnvVarName(service, key string) string {
	return envVarName(service, key)
}

// envVarName builds the environment variable name: CERBERUS_<SERVICE>_<KEY>
// with uppercase and hyphens replaced by underscores.
func envVarName(service, key string) string {
	s := strings.ToUpper(service)
	s = strings.ReplaceAll(s, "-", "_")
	k := strings.ToUpper(key)
	k = strings.ReplaceAll(k, "-", "_")
	return "CERBERUS_" + s + "_" + k
}
