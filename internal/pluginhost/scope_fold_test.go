package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const (
	pluginToken = "q7Zr2mXv9pLwK4tN"     //nolint:gosec // a test sentinel, not a credential
	pluginTeam  = "acme-platform-team"   // a name kept beside the credential
	pluginKey   = "/Users/op/.ssh/id_ed" // a path kept beside the credential
)

// echoProcess is a plugin that puts everything it was handed into what it
// sends back: its result on success, its error otherwise.
type echoProcess struct {
	recordingProcess
	fail bool
}

func (p *echoProcess) CallTool(context.Context, SDKMCPCallRequest) (SDKMCPCallResult, error) {
	cfg := p.initParams.Config
	if p.fail {
		return SDKMCPCallResult{}, errors.New("upstream refused " + cfg["token"] + " for " + cfg["team"] + " reading " + cfg["key_file"])
	}
	content, _ := json.Marshal(map[string]string{"echo": cfg["token"], "team": cfg["team"], "key_file": cfg["key_file"]})
	return SDKMCPCallResult{Content: content}, nil
}

func kindedPlugin() InstalledPlugin {
	plugin := secretDeclaringPlugin()
	plugin.Manifest.Config.Secrets = []contract.SecretRequirement{
		{Name: "token", Env: "CONTEXTFORGE_TOKEN", Required: true},
		{Name: "team", Env: "CONTEXTFORGE_TEAM", Kind: contract.SecretKindName},
		{Name: "key_file", Env: "CONTEXTFORGE_KEY_FILE", Kind: contract.SecretKindPath},
	}
	return plugin
}

func kindedResolver() *fakeResolver {
	return &fakeResolver{values: map[string]string{
		"contextforge/token": pluginToken, "contextforge/team": pluginTeam, "contextforge/key_file": pluginKey,
	}}
}

// WP-S2 for plugins. A plugin's credentials are resolved at load, outside
// any operation's request, so each operation merges them into its own
// scope: its success result — which the plugin redactor never sees — is then
// removed on every surface that renders it. Resolution itself registers
// nothing in the loading request's scope.
func TestPluginCredentialsJoinTheOperationScope(t *testing.T) {
	process := &echoProcess{}
	manager := NewManager(nil, fakeLauncher{process: process}, "test", WithSecretResolver(kindedResolver()))
	manager.RegisterInstalled(kindedPlugin())
	loadCtx, loadScope := redact.EnsureScope(context.Background())
	if err := manager.Load(loadCtx, "contextforge"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = manager.Unload(context.Background(), "contextforge") }()
	if got := loadScope.String(); got != "redact.Scope(0 values)" {
		t.Fatalf("load registered into the loading request's scope: %s", got)
	}

	ctx, scope := redact.EnsureScope(context.Background())
	result, err := manager.ExecuteOperation(ctx, OperationArgs{Connector: "contextforge", Operation: "list_gateways", Config: map[string]any{}})
	if err != nil {
		t.Fatalf("ExecuteOperation: %v", err)
	}
	rendered, err := scope.Marshal(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), pluginToken) || !strings.Contains(string(rendered), redact.Marker) {
		t.Fatalf("success result rendered = %s", rendered)
	}
	// A name and a path are not credentials: they stay in the output.
	if !strings.Contains(string(rendered), pluginTeam) || !strings.Contains(string(rendered), pluginKey) {
		t.Fatalf("a non-credential was redacted: %s", rendered)
	}
}

// The plugin redactor removes credentials from a failure and leaves what the
// manifest declares as a name or a path.
func TestPluginRedactorSkipsNamesAndPaths(t *testing.T) {
	process := &echoProcess{fail: true}
	manager := NewManager(nil, fakeLauncher{process: process}, "test", WithSecretResolver(kindedResolver()))
	manager.RegisterInstalled(kindedPlugin())
	if err := manager.Load(context.Background(), "contextforge"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = manager.Unload(context.Background(), "contextforge") }()

	_, err := manager.ExecuteOperation(context.Background(), OperationArgs{Connector: "contextforge", Operation: "list_gateways", Config: map[string]any{}})
	if err == nil {
		t.Fatal("want the plugin's failure")
	}
	msg := err.Error()
	if strings.Contains(msg, pluginToken) || !strings.Contains(msg, pluginTeam) || !strings.Contains(msg, pluginKey) {
		t.Fatalf("error = %q", msg)
	}
}

// The plugin's stderr reaches the daemon's stderr log redacted, a line at a
// time, so a credential split across two writes is still matched whole.
func TestStderrTapForwardsRedactedLines(t *testing.T) {
	var out bytes.Buffer
	tap := newStderrTap()
	w := tap.wrap(&out)
	tap.setRedactor(redact.New(pluginToken))

	for _, chunk := range []string{"dialing with " + pluginToken[:6], pluginToken[6:] + " now\nhalf a li", "ne\n"} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if got, want := out.String(), "dialing with "+redact.Marker+" now\nhalf a line\n"; got != want {
		t.Fatalf("forwarded %q, want %q", got, want)
	}
}

// A manifest's secret kind is one of the three the host knows.
func TestManifestRefusesAnUnknownSecretKind(t *testing.T) {
	manifest := kindedPlugin().Manifest
	manifest.Config.Secrets[1].Kind = "username"
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), `kind "username"`) {
		t.Fatalf("Validate = %v, want the unknown kind refused", err)
	}
}
