package secrets

import (
	"context"
	"testing"
)

func TestGetReturnsEnvVar(t *testing.T) {
	t.Setenv("CERBERUS_DIGITALOCEAN_API_TOKEN", "do-secret-123")

	p := NewKeychainProvider()
	val, err := p.Get(context.Background(), "digitalocean", "api_token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "do-secret-123" {
		t.Fatalf("expected %q, got %q", "do-secret-123", val)
	}
}

func TestEnvVarNamingConvention(t *testing.T) {
	tests := []struct {
		service string
		key     string
		envVar  string
	}{
		{"digitalocean", "api_token", "CERBERUS_DIGITALOCEAN_API_TOKEN"},
		{"my-service", "secret-key", "CERBERUS_MY_SERVICE_SECRET_KEY"},
		{"GitHub", "Token", "CERBERUS_GITHUB_TOKEN"},
	}

	for _, tt := range tests {
		t.Run(tt.envVar, func(t *testing.T) {
			got := envVarName(tt.service, tt.key)
			if got != tt.envVar {
				t.Errorf("envVarName(%q, %q) = %q, want %q", tt.service, tt.key, got, tt.envVar)
			}
		})
	}
}

func TestGetReturnsEmptyWhenNotFound(t *testing.T) {
	// No env var set, keychain won't have this either in test.
	// We can't easily test keychain absence without mocking, but we can
	// verify the env-var path returns empty when unset.
	p := NewKeychainProvider()
	val, err := p.Get(context.Background(), "nonexistent", "missing_key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "" {
		t.Fatalf("expected empty string, got %q", val)
	}
}

func TestEnvVarHyphensReplacedWithUnderscores(t *testing.T) {
	t.Setenv("CERBERUS_MY_SERVICE_MY_KEY", "hyphen-val")

	p := NewKeychainProvider()
	val, err := p.Get(context.Background(), "my-service", "my-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "hyphen-val" {
		t.Fatalf("expected %q, got %q", "hyphen-val", val)
	}
}

func TestNewKeychainProviderDefaults(t *testing.T) {
	p := NewKeychainProvider()
	if p.serviceName != "cerberus" {
		t.Fatalf("expected service name %q, got %q", "cerberus", p.serviceName)
	}
}

func TestNewKeychainProviderWithService(t *testing.T) {
	p := NewKeychainProviderWithService("custom")
	if p.serviceName != "custom" {
		t.Fatalf("expected service name %q, got %q", "custom", p.serviceName)
	}
}
