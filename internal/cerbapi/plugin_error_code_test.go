package cerbapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/pkg/plugin"
)

// Every code a plugin may send is one the admin lane knows, with a status.
func TestPluginErrorCodesAreHostCodes(t *testing.T) {
	for _, code := range plugin.ErrorCodes {
		if !slices.Contains(externalConnectorErrorCodes, ExternalConnectorErrorCode(code)) {
			t.Errorf("plugin code %q is not in the host vocabulary", code)
		}
	}
}

// ContextForge get_health with the tunnel down and no token: coded
// unreachable (503), not credential_missing, and the whole message — the
// tunnel diagnosis, its recovery step and the missing-token note — survives
// the redaction every surface applies.
func TestPluginUnavailableCodeReachesTheCaller(t *testing.T) {
	args := ExternalConnectorOperationArgs{Connector: "contextforge", Operation: "get_health"}
	diagnosis := "get health: cannot reach ContextForge at http://127.0.0.1:14444 — the tunnel is down, not the gateway. Start the tunnel resource (VPN required)"
	err := managedPluginExecuteError(args, &pluginhost.CodedError{
		Connector:      "contextforge",
		Operation:      "get_health",
		Code:           plugin.ErrorUnavailable,
		Message:        diagnosis,
		MissingSecrets: []string{"token"},
	})
	var external *ExternalConnectorError
	if !errors.As(err, &external) || external.Code != ExternalConnectorUnavailable {
		t.Fatalf("error = %v, want %s", err, ExternalConnectorUnavailable)
	}
	if status := ExternalConnectorHTTPStatus(err, http.StatusTeapot); status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	got := redact.Text(err.Error())
	for _, want := range []string{
		"contextforge get_health: connector_unavailable: " + diagnosis,
		"loaded without the required credential token",
		"cerberus connectors plugin managed load contextforge",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("redacted error lost %q:\n%s", want, got)
		}
	}
}
