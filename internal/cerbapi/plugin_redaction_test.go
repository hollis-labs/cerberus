package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hollis-labs/cerberus/internal/audit"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/connector"
	doconn "github.com/hollis-labs/cerberus/internal/connector/digitalocean"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	gmcp "github.com/hollis-labs/go-mcp/server"
)

// Sentinels for the two ways a credential reaches plugin error text: one the
// host resolved and handed over (value redaction), and one the host never saw
// but that is shaped like an assignment (the redact.Text net).
const (
	resolvedSentinel = "SENTINEL/resolved+token=41c2"
	shapedSentinel   = "SENTINEL-SHAPED-9e07"
)

// echoingPluginProcess is a loaded plugin whose every operation fails with an
// error carrying both sentinels, the resolved one also in its URL-escaped form.
type echoingPluginProcess struct{ token string }

func (p *echoingPluginProcess) Init(_ context.Context, params pluginhost.SDKInitParams) (pluginhost.SDKInitResult, error) {
	p.token = params.Config["token"]
	return pluginhost.SDKInitResult{ID: "leaky", Version: "dev", Protocol: pluginhost.SDKProtocolVersion}, nil
}
func (p *echoingPluginProcess) Load(context.Context) (pluginhost.SDKLoadResult, error) {
	return pluginhost.SDKLoadResult{}, nil
}
func (p *echoingPluginProcess) Unload(context.Context) error { return nil }
func (p *echoingPluginProcess) Health(context.Context) (pluginhost.SDKHealthResult, error) {
	return pluginhost.SDKHealthResult{OK: true}, nil
}
func (p *echoingPluginProcess) CallTool(context.Context, pluginhost.SDKMCPCallRequest) (pluginhost.SDKMCPCallResult, error) {
	return pluginhost.SDKMCPCallResult{}, fmt.Errorf("upstream rejected %s via https://api.example.test/?key=%s; api_key=%s",
		p.token, url.QueryEscape(p.token), shapedSentinel)
}
func (p *echoingPluginProcess) Close() error { return nil }

type echoingLauncher struct{ process pluginhost.Process }

func (l echoingLauncher) Launch(context.Context, pluginhost.InstalledPlugin) (pluginhost.Process, error) {
	return l.process, nil
}

type sentinelResolver struct{}

func (sentinelResolver) Get(_ context.Context, service, key string) (string, error) {
	if service == "leaky" && key == "token" {
		return resolvedSentinel, nil
	}
	return "", nil
}

// leakyManagedService is a managed plugin lane with one loaded plugin, "leaky",
// that echoes its credential into every error.
func leakyManagedService(t *testing.T) *ManagedPluginConnectorService {
	t.Helper()
	svc, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", io.Discard, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewManagedPluginConnectorService: %v", err)
	}
	svc.manager = pluginhost.NewManager(nil, echoingLauncher{process: &echoingPluginProcess{}}, "test",
		pluginhost.WithSecretResolver(sentinelResolver{}))
	svc.manager.RegisterInstalled(pluginhost.InstalledPlugin{
		ID:     "leaky",
		Origin: pluginhost.OriginInstalled,
		Manifest: contract.Manifest{
			APIVersion: contract.ManifestAPIVersion,
			Kind:       "Connector",
			ID:         "leaky",
			Version:    "dev",
			Config: contract.ConfigSchema{Secrets: []contract.SecretRequirement{
				{Name: "token", Env: "CERBERUS_LEAKY_TOKEN"},
			}},
			Operations: []contract.ManifestOperation{
				{Name: "list_things", Effect: contract.EffectRead, InputSchema: contract.ObjectSchema(map[string]any{})},
			},
		},
	})
	if err := svc.manager.Load(context.Background(), "leaky"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return svc
}

func assertNoPluginSentinel(t *testing.T, where, text string) {
	t.Helper()
	for _, form := range []string{resolvedSentinel, url.QueryEscape(resolvedSentinel), shapedSentinel} {
		if strings.Contains(text, form) {
			t.Fatalf("%s leaked %q:\n%s", where, form, text)
		}
	}
}

// capturingContext records every MCP notification an operation sends.
func capturingContext() (context.Context, *[]string) {
	var messages []string
	ctx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) {
		data, _ := json.Marshal(n.Params)
		messages = append(messages, string(data))
	})
	return ctx, &messages
}

// Both routes to a managed plugin — the admin lane and the direct managed
// exec — must keep the plugin's credential out of the error the caller gets
// and out of the MCP notifications sent along the way.
func TestManagedPluginFailureTextIsRedactedOnEveryRoute(t *testing.T) {
	routes := map[string]func(context.Context, *ManagedPluginConnectorService) error{
		"admin lane": func(ctx context.Context, svc *ManagedPluginConnectorService) error {
			_, err := NewExternalConnectorService(audit.NewMemory(), connector.NewRegistry(), svc).Execute(ctx,
				ExternalConnectorOperationArgs{Connector: "leaky", Operation: "list_things"})
			return err
		},
		"managed exec": func(ctx context.Context, svc *ManagedPluginConnectorService) error {
			_, err := svc.Execute(ctx, "leaky", PluginConnectorExecArgs{Operation: "list_things"})
			return err
		},
	}
	for name, run := range routes {
		t.Run(name, func(t *testing.T) {
			ctx, notifications := capturingContext()
			err := run(ctx, leakyManagedService(t))
			if err == nil {
				t.Fatal("operation succeeded, want the plugin's failure")
			}
			var coded *ExternalConnectorError
			if !errors.As(err, &coded) || coded.Code != ExternalConnectorOperationFailed {
				t.Fatalf("error = %v, want an ExternalConnectorError coded %s", err, ExternalConnectorOperationFailed)
			}
			assertNoPluginSentinel(t, "returned error", err.Error())
			if !strings.Contains(err.Error(), "upstream rejected") {
				t.Fatalf("error lost the plugin's message: %v", err)
			}
			if len(*notifications) == 0 {
				t.Fatal("no notifications captured; the test is not observing the notify path")
			}
			for _, message := range *notifications {
				assertNoPluginSentinel(t, "notification", message)
			}
		})
	}
}

// operation_failed is Cerberus's own code, so it must survive the redaction it
// exists to route text through, and the message after it must survive too.
func TestOperationFailedCodeSurvivesRedaction(t *testing.T) {
	err := managedPluginExecuteError(
		ExternalConnectorOperationArgs{Connector: "leaky", Operation: "list_things"},
		errors.New("reload the plugin and retry"))
	if got := err.Error(); !strings.Contains(got, "operation_failed: reload the plugin and retry") {
		t.Fatalf("redaction ate the code or its message: %q", got)
	}
}

// A dry-run preview of create_droplet lands in agent context and logs. Its
// cloud-init user_data routinely carries credentials, so the preview describes
// the script by size and hash and never carries it.
func TestDigitalOceanCreateDropletPreviewNeverCarriesUserData(t *testing.T) {
	const userData = "#cloud-config\nwrite_files:\n  - content: SENTINEL-USER-DATA-5b1d\n"
	registry := connector.NewRegistry()
	registry.RegisterDefinition(doconn.Definition())
	svc := NewExternalConnectorService(audit.NewMemory(), registry)
	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "digitalocean",
		Operation: "create_droplet",
		DryRun:    true,
		Config: map[string]any{
			"name": "web-1", "region": "nyc3", "size": "s-1vcpu-1gb", "image": "ubuntu-24-04-x64",
			"user_data": userData,
		},
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "SENTINEL-USER-DATA-5b1d") || strings.Contains(string(encoded), "cloud-config") {
		t.Fatalf("preview carries the user_data content:\n%s", encoded)
	}
	preview := result.Data.(ExternalConnectorDryRunPreview)
	digest, ok := preview.Input["user_data"].(map[string]any)
	if !ok {
		t.Fatalf("user_data = %#v, want a size and hash", preview.Input["user_data"])
	}
	if digest["bytes"] != len(userData) {
		t.Fatalf("bytes = %v, want %d", digest["bytes"], len(userData))
	}
	if sum, _ := digest["sha256"].(string); len(sum) != 64 {
		t.Fatalf("sha256 = %v, want a hex digest", digest["sha256"])
	}
}
