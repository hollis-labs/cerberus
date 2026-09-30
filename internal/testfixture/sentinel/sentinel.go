// Package sentinel is the WP-S2 acceptance fixture: a credential that
// matches no redaction rule, and a real connector that fails by echoing it
// back in text it did not compose. It is imported only by tests.
//
// The acceptance criterion it serves: a credential resolved during an
// operation cannot appear in that operation's error text even when the
// message is composed by someone else. Only value redaction at the request
// scope can meet it; the regex net provably misses Value.
//
// Every credentialed provider connector is a plugin, so the fixture is one:
// a managed plugin, installed through the review and launched as a real
// subprocess (the test binary, re-executed), whose token the host resolves
// through the registering provider and hands over at load. Its one operation
// calls an upstream API that rejects the token with a 401 echoing it back,
// unlabelled, and the plugin wraps that body in vendor-style error text
// without scrubbing anything. That is the worst case a third-party plugin
// can present, and the host is the only thing left to catch it.
package sentinel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/connector"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secrets"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	secret "github.com/hollis-labs/cerberus/pkg/secret"
	sdksubprocess "github.com/hollis-labs/plugin-sdk/subprocess"
)

// Value is the resolved credential: no provider prefix, no label, not
// Bearer-shaped. Rendered with no scope it survives redact.Text.
const Value = "q7Zr2mXv9pLwK4tN" //nolint:gosec // a test sentinel, not a credential

// ConnectorID is the fixture plugin's id, and Operation its one operation:
// a read, so it needs no acknowledgment.
const (
	ConnectorID = "sentinel"
	Operation   = "status"
)

// ToolName is the MCP tool the host generates for Operation once it is
// exposed, as it does for any plugin operation.
const ToolName = "cerberus_sentinel_status"

// Environment the helper process reads. The plugin launch environment passes
// GO_WANT_* through for exactly this kind of test helper.
const (
	envHelper = "GO_WANT_SENTINEL_PLUGIN"
	envAPI    = "GO_WANT_SENTINEL_API"
)

// Args is the operation's input.
func Args() map[string]any { return map[string]any{"owner": "hollis-labs", "repo": "cerberus"} }

// UpstreamAPI is an API that rejects every request with a 401 whose message
// echoes the bearer token it was sent, unlabelled.
func UpstreamAPI(t testing.TB) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Bad credentials for " + token + " on this repo"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ServeHelper runs the fixture plugin when this test binary was launched as
// it. Each package that uses the fixture calls it from a test named
// helperTest (see Service), and it returns at once in an ordinary run.
func ServeHelper() {
	if os.Getenv(envHelper) != "1" {
		return
	}
	if err := sdksubprocess.Serve(&echoPlugin{api: os.Getenv(envAPI)}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// Service is the admin lane with the fixture plugin installed, loaded and its
// operation exposed over MCP. provider supplies its token (Provider, for the
// acceptance test); helperTest names the test in the calling package that
// calls ServeHelper, which the plugin's entrypoint re-executes the test
// binary into. It also returns the managed lane, for an in-process client.
func Service(t *testing.T, sink audit.Sink, provider secret.Reader, apiURL, helperTest string) (*cerbapi.ExternalConnectorService, *cerbapi.ManagedPluginConnectorService) {
	t.Helper()
	t.Setenv(envHelper, "1")
	t.Setenv(envAPI, apiURL)

	root, err := os.MkdirTemp("/tmp", "cerbsent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	statePath := filepath.Join(root, "plugin-connectors.json")
	configPath := filepath.Join(root, pluginhost.ConnectorConfigFilename)
	if err = os.WriteFile(configPath, []byte(ConnectorID+":\n  mcp:\n    expose: ["+Operation+"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := cerbapi.WithCallerSurface(context.Background(), cerbapi.SurfaceInProcess)
	reviewer := cerbapi.NewPluginReviewer(sink, statePath)
	pending, err := reviewer.PrepareInstall(ctx, pluginDir(t, root, helperTest), false)
	if err != nil {
		t.Fatalf("install review: %v", err)
	}
	if _, err = reviewer.Accept(ctx, pending, pending.Review.ID); err != nil {
		t.Fatalf("accept install: %v", err)
	}
	managed, err := cerbapi.NewManagedPluginConnectorService(sink, "test", io.Discard, statePath,
		cerbapi.WithManagedPluginSecrets(provider), cerbapi.WithManagedPluginConnectorConfig(configPath))
	if err != nil {
		t.Fatalf("managed plugin lane: %v", err)
	}
	if _, err := managed.Load(ctx, ConnectorID); err != nil {
		t.Fatalf("load %s: %v", ConnectorID, err)
	}
	t.Cleanup(func() { _, _ = managed.Unload(context.Background(), ConnectorID) })
	return cerbapi.NewExternalConnectorService(sink, connector.NewRegistry(), managed), managed
}

// Provider holds Value as sentinel/token behind secrets.Registering, the
// wrapper app.ConnectorSecrets uses, so resolving it registers it for value
// redaction.
func Provider() secret.Reader {
	return secrets.Registering(staticProvider{ConnectorID + "/token": Value}, nil)
}

// AssertAbsent fails when rendered carries Value, and when it carries no
// redaction marker either — a surface that dropped the message entirely
// proves nothing.
func AssertAbsent(t testing.TB, surface, rendered string) {
	t.Helper()
	if strings.Contains(rendered, Value) {
		t.Errorf("%s: the resolved credential reached the output: %s", surface, rendered)
	}
	if !strings.Contains(rendered, redact.Marker) {
		t.Errorf("%s: no redaction marker, so nothing was proven: %s", surface, rendered)
	}
}

// pluginDir writes an installable plugin: plugin.yaml, and an entrypoint
// script that execs this test binary into helperTest (a bundle refuses
// symlinks).
func pluginDir(t *testing.T, root, helperTest string) string {
	t.Helper()
	dir := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", `'\''`) + "' -test.run='^" + helperTest + "$' \"$@\"\n"
	if err = os.WriteFile(filepath.Join(dir, "bin", "plugin"), []byte(script), 0o755); err != nil { //nolint:gosec // test plugin entrypoint
		t.Fatal(err)
	}
	spec := pluginhost.PluginYAMLFromManifest(contract.ManifestFromDefinition(definition()), pluginhost.Entrypoint{Command: "bin/plugin"})
	data, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pluginhost.PluginYAMLFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       "1.0.0",
		ResourceTypes: []string{"repository"},
		Config: contract.ConfigSchema{Secrets: []contract.SecretRequirement{
			{Name: "token", Env: "CERBERUS_SENTINEL_TOKEN", Required: true},
		}},
		Operations: []contract.Operation{{
			Name:         Operation,
			Description:  "Read a repository's status from an API that rejects the token.",
			Effect:       contract.EffectRead,
			Target:       contract.TargetDescriptor{Kind: "sentinel.repo", From: []string{"owner", "repo"}},
			Preview:      contract.PreviewNone,
			Output:       contract.OutputStructured,
			OutputSchema: map[string]any{"type": "object"},
			Cost:         contract.CostNone,
			LocalFS:      contract.LocalFSNone,
			InputSchema: contract.ObjectSchema(map[string]any{
				"owner": contract.StringSchema("Owner."),
				"repo":  contract.StringSchema("Repository."),
			}, "owner", "repo"),
		}},
	})
}

// echoPlugin holds the token the host handed it and sends it upstream as a
// bearer token. It scrubs nothing, which a well-behaved plugin would.
type echoPlugin struct {
	api   string
	token string
}

func (p *echoPlugin) Init(_ context.Context, params sdksubprocess.InitParams) (sdksubprocess.InitResult, error) {
	p.token = params.Config["token"]
	return sdksubprocess.InitResult{ID: ConnectorID, Name: "WP-S2 sentinel", Version: "1.0.0", Protocol: sdksubprocess.ProtocolVersion}, nil
}

func (p *echoPlugin) Load(context.Context) (sdksubprocess.LoadResult, error) {
	return sdksubprocess.LoadResult{}, nil
}

func (p *echoPlugin) Unload(context.Context) error { return nil }

func (p *echoPlugin) Health(context.Context) (sdksubprocess.HealthStatus, error) {
	return sdksubprocess.HealthStatus{OK: true}, nil
}

// MCPCallTool fails the way an SDK does on a 401: the method, the URL and the
// upstream's body, which here carries the token with no label.
func (p *echoPlugin) MCPCallTool(ctx context.Context, req sdksubprocess.MCPCallRequest) (sdksubprocess.MCPCallResult, error) {
	owner, _ := req.Arguments["owner"].(string)
	repo, _ := req.Arguments["repo"].(string)
	target := p.api + "/repos/" + owner + "/" + repo
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return sdksubprocess.MCPCallResult{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return sdksubprocess.MCPCallResult{}, err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &msg)
		return sdksubprocess.MCPCallResult{}, fmt.Errorf("GET %s: %d %s", target, resp.StatusCode, msg.Message)
	}
	return sdksubprocess.MCPCallResult{}, errors.New("the sentinel upstream accepted a request; it must always refuse")
}

type staticProvider map[string]string

func (p staticProvider) Get(_ context.Context, service, key string) (string, error) {
	return p[service+"/"+key], nil
}
func (staticProvider) Set(context.Context, string, string, string) error { return nil }
func (staticProvider) Delete(context.Context, string, string) error      { return nil }
