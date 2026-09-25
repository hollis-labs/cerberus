package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/connector"
	cfconn "github.com/hollis-labs/cerberus/internal/connector/cloudflare"
	doconn "github.com/hollis-labs/cerberus/internal/connector/digitalocean"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	forgeconn "github.com/hollis-labs/cerberus/internal/connector/forge"
	ghconn "github.com/hollis-labs/cerberus/internal/connector/github"
	ncconn "github.com/hollis-labs/cerberus/internal/connector/namecheap"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func builtinConnectorDefinitions() []contract.Definition {
	return []contract.Definition{
		cfconn.Definition(),
		doconn.Definition(),
		dockerconn.Definition(),
		forgeconn.Definition(),
		ghconn.Definition(),
		ncconn.Definition(),
		sshconn.Definition(),
	}
}

// resolveCountingRegistry registers every built-in definition behind a
// factory that only counts. Real dispatch starts with Resolve, so a count of
// zero proves no call reached a connector.
func resolveCountingRegistry(resolves *int) *connector.Registry {
	registry := connector.NewRegistry()
	for _, def := range builtinConnectorDefinitions() {
		registry.RegisterFactory(def, func(context.Context) (contract.Connector, error) {
			*resolves++
			return nil, errors.New("resolve reached: a dry run must never get here")
		})
	}
	return registry
}

// sampleConfig fills every property of an operation's schema with a
// plausible value, so a preview that exists can build rather than failing
// on a missing argument.
func sampleConfig(op contract.Operation) map[string]any {
	cfg := map[string]any{}
	props, _ := op.InputSchema["properties"].(map[string]any)
	for name, raw := range props {
		schema, _ := raw.(map[string]any)
		switch schema["type"] {
		case "integer", "number":
			cfg[name] = 1
		case "boolean":
			cfg[name] = false
		case "array":
			cfg[name] = []any{"sample"}
		case "object":
			cfg[name] = map[string]any{}
		default:
			cfg[name] = "sample"
		}
	}
	return cfg
}

// sweepConfig is sampleConfig, except that an SSH operation names its
// configured target by id, which is all the SSH lane accepts.
func sweepConfig(connectorID string, op contract.Operation) map[string]any {
	cfg := sampleConfig(op)
	if connectorID != "ssh" {
		return cfg
	}
	out := map[string]any{"id": "server-1"}
	for key, value := range cfg {
		if sshOperationFields[key] && key != "id" {
			out[key] = value
		}
	}
	return out
}

func connectorErrorCode(err error) ExternalConnectorErrorCode {
	var connErr *ExternalConnectorError
	if errors.As(err, &connErr) {
		return connErr.Code
	}
	return ""
}

// TestDryRunNeverExecutes is the fix for "docker destroy --dry-run --ack
// destroys": every declared operation, with dry_run and ack both set, must
// stop before the connector is resolved. An operation that declares
// supports_dry must be recognized by a preview; one that does not must be
// refused as preview_unsupported.
func TestDryRunNeverExecutes(t *testing.T) {
	var resolves int
	svc := NewExternalConnectorService(resolveCountingRegistry(&resolves))
	svc.SetResourceLookup(sshTestLookup())

	for _, def := range svc.Definitions() {
		for _, op := range def.Operations {
			name := def.ID + "." + op.Name
			_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
				Connector:    def.ID,
				Operation:    op.Name,
				Config:       sweepConfig(def.ID, op),
				DryRun:       true,
				Acknowledged: true,
			})
			code := connectorErrorCode(err)
			switch {
			case code == ExternalConnectorUnsupported:
				// Refused outright before previews (namecheap per-record writes).
			case op.SupportsDry && code == ExternalConnectorPreviewUnsupported:
				t.Errorf("%s declares supports_dry but has no preview", name)
			case !op.SupportsDry && code != ExternalConnectorPreviewUnsupported:
				t.Errorf("%s has no preview but dry run returned %v (code %q), want preview_unsupported", name, err, code)
			}
			if resolves != 0 {
				t.Fatalf("%s: dry run resolved the connector", name)
			}
		}
	}
}

// TestDryRunNeverExecutesAgainstFakes runs the same sweep against real
// connectors over recording fakes, for the operations where a fake records
// the mutation.
func TestDryRunNeverExecutesAgainstFakes(t *testing.T) {
	docker := &fakeDockerBackend{}
	do := &fakeDigitalOceanBackend{}
	forge := &fakeForgeBackend{}
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(docker))
	registry.Register(doconn.NewWithBackend(do))
	registry.Register(forgeconn.NewWithBackend(forge))
	svc := NewExternalConnectorService(registry)

	for _, def := range svc.Definitions() {
		for _, op := range def.Operations {
			_, _ = svc.Execute(context.Background(), ExternalConnectorOperationArgs{
				Connector: def.ID, Operation: op.Name, Config: sweepConfig(def.ID, op), DryRun: true, Acknowledged: true,
			})
		}
	}
	// A dry run calls nothing on the backend, reads included.
	if docker.started != "" || docker.logName != "" {
		t.Errorf("docker backend called under dry run: %+v", docker)
	}
	if do.created != nil || do.dropletID != 0 {
		t.Errorf("digitalocean backend called under dry run: %+v", do)
	}
	if forge.serverID != 0 || forge.siteID != 0 || forge.command != "" {
		t.Errorf("forge backend called under dry run: %+v", forge)
	}
}

func TestAckGateFailsClosedWithoutDefinition(t *testing.T) {
	svc := NewExternalConnectorService(connector.NewRegistry())
	err := svc.requireAcknowledgment(ExternalConnectorOperationArgs{Connector: "ghost", Operation: "wipe"})
	if code := connectorErrorCode(err); code != ExternalConnectorUnsupported {
		t.Fatalf("missing definition: err = %v, want operation_unsupported refusal", err)
	}
}

func TestAckGateFailsClosedOnUndeclaredOperation(t *testing.T) {
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(&fakeDockerBackend{}))
	svc := NewExternalConnectorService(registry)

	// executeDocker dispatches "status", but the definition does not declare
	// it, so the gate cannot say whether it is destructive.
	_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{Connector: "docker", Operation: "status", Acknowledged: true})
	if code := connectorErrorCode(err); code != ExternalConnectorUnsupported {
		t.Fatalf("undeclared operation: err = %v, want operation_unsupported refusal", err)
	}
}

func TestAckGateStillRequiresAckForDeclaredDestructive(t *testing.T) {
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(&fakeDockerBackend{}))
	svc := NewExternalConnectorService(registry)
	_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{Connector: "docker", Operation: "destroy", Config: map[string]any{"container": "web"}})
	if code := connectorErrorCode(err); code != ExternalConnectorAckRequired {
		t.Fatalf("err = %v, want acknowledgment_required", err)
	}
}

// Newly destructive operations (P0-2 item 8) refuse without ack.
func TestReclassifiedOperationsRequireAck(t *testing.T) {
	for _, tc := range []struct{ connector, operation string }{
		{"forge", "update_deployment_script"},
		{"digitalocean", "create_droplet"},
		{"digitalocean", "stop"},
	} {
		found := false
		for _, def := range builtinConnectorDefinitions() {
			for _, op := range def.Operations {
				if def.ID == tc.connector && op.Name == tc.operation {
					found = true
					if !op.Destructive {
						t.Errorf("%s.%s must be destructive", tc.connector, tc.operation)
					}
				}
			}
		}
		if !found {
			t.Errorf("%s.%s not declared", tc.connector, tc.operation)
		}
	}
}

// Every error code, and the refusal text behind each new one, must survive
// redaction: main prints errors through redact.Text, and a gate whose
// explanation is eaten is worse than no explanation.
func TestGateRefusalsSurviveRedaction(t *testing.T) {
	for _, code := range externalConnectorErrorCodes {
		msg := "docker destroy: " + string(code) + ": reload the plugin and retry"
		if got := redact.Text(msg); got != msg {
			t.Errorf("code %s: redact.Text changed\n got %q\nwant %q", code, got, msg)
		}
	}

	svc := NewExternalConnectorService(connector.NewRegistry())
	args := ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers"}
	for _, err := range []error{
		previewUnsupportedError(args),
		svc.requireAcknowledgment(ExternalConnectorOperationArgs{Connector: "ghost", Operation: "wipe"}),
	} {
		var connErr *ExternalConnectorError
		if !errors.As(err, &connErr) {
			t.Fatalf("not a connector error: %v", err)
		}
		raw := connErr.Connector + " " + connErr.Operation + ": " + string(connErr.Code) + ": " + connErr.Err.Error()
		if got := err.Error(); got != raw {
			t.Errorf("refusal changed by redaction:\n got %q\nwant %q", got, raw)
		}
		if !strings.Contains(err.Error(), "refusing") && !strings.Contains(err.Error(), "nothing was executed") {
			t.Errorf("refusal lost its explanation: %q", err.Error())
		}
	}
}

func TestManagedPluginPreviewUnsupportedKeepsItsCode(t *testing.T) {
	args := ExternalConnectorOperationArgs{Connector: "k8s", Operation: "restart", DryRun: true}
	err := managedPluginExecuteError(args, fmt.Errorf("plugin %q operation %q: %w", "k8s", "restart", pluginhost.ErrPreviewUnsupported))
	if code := connectorErrorCode(err); code != ExternalConnectorPreviewUnsupported {
		t.Fatalf("err = %v, want preview_unsupported", err)
	}
}
