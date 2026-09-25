package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/hollis-labs/cerberus/internal/audit"
	"testing"

	"github.com/hollis-labs/cerberus/internal/connector"
	doconn "github.com/hollis-labs/cerberus/internal/connector/digitalocean"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// noSecrets is a credential store with nothing in it.
type noSecrets struct{}

func (noSecrets) Get(context.Context, string, string) (string, error) { return "", nil }
func (noSecrets) Set(context.Context, string, string, string) error   { return nil }
func (noSecrets) Delete(context.Context, string, string) error        { return nil }

// An acknowledgment refusal never depends on having a credential. With no
// token and no ack, a destructive DigitalOcean operation is refused as
// acknowledgment_required, and the connector — which is what reads the
// token — is never constructed. With the ack, the same call reaches
// resolution and reports the missing token.
func TestAckRefusalComesBeforeCredentialResolution(t *testing.T) {
	resolves := 0
	registry := connector.NewRegistry()
	registry.RegisterFactory(doconn.Definition(), func(context.Context) (contract.Connector, error) {
		resolves++
		return doconn.New(noSecrets{})
	})
	svc := NewExternalConnectorService(audit.NewMemory(), registry)

	for _, op := range []string{"stop", "destroy", "create_droplet"} {
		t.Run(op, func(t *testing.T) {
			resolves = 0
			declared, _ := doconn.Definition().Operation(op)
			args := ExternalConnectorOperationArgs{Connector: "digitalocean", Operation: op, Config: sampleConfig(declared)}

			_, err := svc.Execute(context.Background(), args)
			var connErr *ExternalConnectorError
			if !errors.As(err, &connErr) || connErr.Code != ExternalConnectorAckRequired {
				t.Fatalf("un-acked %s: got %v, want acknowledgment_required", op, err)
			}
			if resolves != 0 {
				t.Fatalf("un-acked %s resolved the connector %d times", op, resolves)
			}
			if got := redact.Text(err.Error()); got != err.Error() {
				t.Fatalf("refusal changed by redaction:\n got %q\nwant %q", got, err.Error())
			}

			args.Acknowledged = true
			_, err = svc.Execute(context.Background(), args)
			if !errors.As(err, &connErr) || connErr.Code != ExternalConnectorCredentialMissing {
				t.Fatalf("acked %s: got %v, want credential_missing", op, err)
			}
		})
	}
}

// The plugin host refuses an un-acked destructive operation, and an
// undeclared one, before calling the plugin. The admin lane reports each with
// its code, so every surface maps it the way it maps the built-in refusal.
func TestPluginRefusalsCarryTheirCodes(t *testing.T) {
	args := ExternalConnectorOperationArgs{Connector: "kubernetes", Operation: "delete_pod"}
	ackErr := pluginhost.OperationAllowed(pluginhost.OriginInstalled, contract.ManifestOperation{Name: "delete_pod", Destructive: true}, false)
	if !errors.Is(ackErr, pluginhost.ErrAckRequired) {
		t.Fatalf("OperationAllowed: %v, want ErrAckRequired", ackErr)
	}
	for _, tc := range []struct {
		err  error
		want ExternalConnectorErrorCode
	}{
		{ackErr, ExternalConnectorAckRequired},
		{fmt.Errorf("plugin %q: %w %q", "kubernetes", pluginhost.ErrOperationUndeclared, "wipe"), ExternalConnectorUnsupported},
	} {
		err := managedPluginExecuteError(args, tc.err)
		var connErr *ExternalConnectorError
		if !errors.As(err, &connErr) || connErr.Code != tc.want {
			t.Fatalf("%v: got %v, want %s", tc.err, err, tc.want)
		}
		if got := redact.Text(err.Error()); got != err.Error() {
			t.Fatalf("refusal changed by redaction:\n got %q\nwant %q", got, err.Error())
		}
		// Coding twice, once per layer, leaves one code and one prefix.
		if again := managedPluginExecuteError(args, err); again.Error() != err.Error() {
			t.Fatalf("recoded:\n got %q\nwant %q", again.Error(), err.Error())
		}
	}
}
