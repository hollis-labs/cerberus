package cerbapi

import (
	"context"
	"errors"
	"github.com/hollis-labs/cerberus/internal/audit"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
)

// A plugin that fails for want of a credential it declared must report the
// same code a built-in connector does. Before WP-7 the plugin lane had no
// credential story at all, so this code was unreachable through a plugin.
func TestManagedPluginCredentialFailureReportsCredentialMissing(t *testing.T) {
	args := ExternalConnectorOperationArgs{Connector: "contextforge", Operation: "list_gateways"}
	err := managedPluginExecuteError(args, &pluginhost.MissingSecretsError{
		Connector: "contextforge",
		Secrets:   []string{"token"},
		Err:       errors.New("401 Unauthorized"),
	})

	var external *ExternalConnectorError
	if !errors.As(err, &external) {
		t.Fatalf("error = %v, want an ExternalConnectorError", err)
	}
	if external.Code != ExternalConnectorCredentialMissing {
		t.Fatalf("Code = %q, want %q", external.Code, ExternalConnectorCredentialMissing)
	}
	if !strings.Contains(err.Error(), "CERBERUS_CONTEXTFORGE_TOKEN") {
		t.Fatalf("error %q should name the variable that supplies the credential", err.Error())
	}
}

// Every other plugin failure is coded operation_failed, never
// credential_missing — classifying broadly would make that code meaningless —
// and keeps the plugin's error in its chain.
func TestManagedPluginOtherFailuresAreNotReclassified(t *testing.T) {
	args := ExternalConnectorOperationArgs{Connector: "contextforge", Operation: "list_gateways"}
	original := errors.New("gateway unreachable")
	got := managedPluginExecuteError(args, original)
	if !errors.Is(got, original) {
		t.Fatalf("error = %v, want the original error in its chain", got)
	}
	var external *ExternalConnectorError
	if !errors.As(got, &external) || external.Code != ExternalConnectorOperationFailed {
		t.Fatalf("error = %v, want code %s", got, ExternalConnectorOperationFailed)
	}
}

// The managed lane must accept a secret resolver, and must still work without
// one — a daemon that cannot reach the keychain still serves its plugins.
func TestManagedPluginServiceAcceptsSecretResolver(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	svc, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", io.Discard, statePath,
		WithManagedPluginSecrets(stubResolver{}))
	if err != nil {
		t.Fatalf("NewManagedPluginConnectorService: %v", err)
	}
	if svc.manager == nil {
		t.Fatal("manager was not constructed")
	}
	if got := svc.manager.MissingSecrets("contextforge"); got != nil {
		t.Fatalf("MissingSecrets = %v, want nil for a plugin that is not loaded", got)
	}
}

type stubResolver struct{}

func (stubResolver) Get(context.Context, string, string) (string, error) { return "", nil }
