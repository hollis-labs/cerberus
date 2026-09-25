package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func writeConnectorConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ConnectorConfigFilename)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// configuredPlugin declares one field of each type the file has to check,
// and two operations.
func configuredPlugin() InstalledPlugin {
	plugin := secretDeclaringPlugin()
	plugin.Manifest.Config.Fields = []contract.ConfigField{
		{Name: "address", Type: "string"},
		{Name: "sandbox", Type: "boolean"},
		{Name: "timeout", Type: "integer"},
		{Name: "paths", Type: "array"},
	}
	return plugin
}

func TestLoadConnectorConfigAbsentOrEmptyIsNoSettings(t *testing.T) {
	cfg, err := LoadConnectorConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil || len(cfg.Entries) != 0 || cfg.SHA256 != "" {
		t.Fatalf("absent file: cfg = %+v, err = %v", cfg, err)
	}
	cfg, err = LoadConnectorConfig(writeConnectorConfig(t, "", 0o600))
	if err != nil || len(cfg.Entries) != 0 {
		t.Fatalf("empty file: cfg = %+v, err = %v", cfg, err)
	}
	settings := cfg.ForPlugin(configuredPlugin())
	if len(settings.Problems)+len(settings.Fields)+len(settings.Expose) != 0 {
		t.Fatalf("no entry must mean no settings: %+v", settings)
	}
}

func TestConnectorConfigDeliversDeclaredFieldsTyped(t *testing.T) {
	body := `contextforge:
  fields:
    address: http://127.0.0.1:14444
    sandbox: true
    timeout: 30
    paths: [/opt/bin, /usr/local/bin]
  mcp:
    expose: [list_gateways, get_health, list_gateways]
`
	path := writeConnectorConfig(t, body, 0o600)
	cfg, err := LoadConnectorConfig(path)
	if err != nil {
		t.Fatalf("LoadConnectorConfig: %v", err)
	}
	sum := sha256.Sum256([]byte(body))
	if cfg.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("SHA256 = %s, want the file's", cfg.SHA256)
	}
	settings := cfg.ForPlugin(configuredPlugin())
	if len(settings.Problems) != 0 {
		t.Fatalf("problems = %v", settings.Problems)
	}
	want := map[string]string{
		"address": "http://127.0.0.1:14444",
		"sandbox": "true",
		"timeout": "30",
		"paths":   `["/opt/bin","/usr/local/bin"]`,
	}
	for name, value := range want {
		if settings.Config[name] != value {
			t.Errorf("Config[%s] = %q, want %q", name, settings.Config[name], value)
		}
	}
	if strings.Join(settings.Fields, ",") != "address,paths,sandbox,timeout" {
		t.Errorf("Fields = %v", settings.Fields)
	}
	if strings.Join(settings.Expose, ",") != "get_health,list_gateways" {
		t.Errorf("Expose = %v, want sorted and deduplicated", settings.Expose)
	}
}

// Nothing is silently dropped: each refusal names what it refused and what
// would have been accepted.
func TestConnectorConfigRefusals(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"undeclared field", "contextforge:\n  fields: {server: https://x}\n", `field "server" is not declared by the plugin (declared: address, paths, sandbox, timeout)`},
		{"wrong type", "contextforge:\n  fields: {sandbox: \"yes\"}\n", `field "sandbox": want true or false`},
		{"fractional integer", "contextforge:\n  fields: {timeout: 1.5}\n", `field "timeout": want a whole number`},
		{"keychain reference", "contextforge:\n  fields: {address: keychain://contextforge/address}\n", "references belong in connector-secrets.yaml"},
		{"nested helper reference", "contextforge:\n  fields: {paths: [/bin, \"helper://h/a/b\"]}\n", "secret reference (helper://)"},
		{"1Password reference", "contextforge:\n  fields: {address: op://vault/item/field}\n", "secret reference (op://)"},
		{"undeclared operation", "contextforge:\n  mcp: {expose: [delete_everything]}\n", `mcp.expose names "delete_everything", which the plugin does not declare (operations: get_health, list_gateways)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadConnectorConfig(writeConnectorConfig(t, tc.body, 0o600))
			if err != nil {
				t.Fatalf("LoadConnectorConfig: %v", err)
			}
			problems := strings.Join(cfg.ForPlugin(configuredPlugin()).Problems, "\n")
			if !strings.Contains(problems, tc.want) {
				t.Fatalf("problems = %q, want %q", problems, tc.want)
			}
			// A refusal is operator-facing text; it must arrive intact.
			if got := redact.Text(problems); got != problems {
				t.Fatalf("redaction mangled the refusal:\n in: %s\ngot: %s", problems, got)
			}
		})
	}
}

// A misspelled key is an error, not an ignored setting.
func TestConnectorConfigUnknownKeyIsAnError(t *testing.T) {
	_, err := LoadConnectorConfig(writeConnectorConfig(t, "contextforge:\n  feilds: {address: x}\n", 0o600))
	if err == nil || !strings.Contains(err.Error(), "feilds") {
		t.Fatalf("err = %v, want the unknown key named", err)
	}
}

func TestConnectorConfigWarnsWhenWritableByOthers(t *testing.T) {
	cfg, err := LoadConnectorConfig(writeConnectorConfig(t, "contextforge: {}\n", 0o666))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "group- or world-writable") {
		t.Fatalf("warnings = %v", cfg.Warnings)
	}
	cfg, _ = LoadConnectorConfig(writeConnectorConfig(t, "contextforge: {}\n", 0o644))
	if len(cfg.Warnings) != 0 {
		t.Fatalf("0644 must not warn: %v", cfg.Warnings)
	}
}

func configManager(t *testing.T, path string, process Process, warnings *[]string) *Manager {
	t.Helper()
	resolver := &fakeResolver{values: map[string]string{"contextforge/token": "jwt-value"}}
	manager := NewManager(nil, fakeLauncher{process: process}, "test",
		WithSecretResolver(resolver),
		WithConnectorConfig(func() (ConnectorConfig, error) { return LoadConnectorConfig(path) }),
		WithLoadWarning(func(line string) { *warnings = append(*warnings, line) }))
	manager.RegisterInstalled(configuredPlugin())
	return manager
}

// Fields reach Init beside the secrets, and what was delivered is reported
// by name with the file's fingerprint.
func TestManagerDeliversFieldsBesideSecrets(t *testing.T) {
	path := writeConnectorConfig(t, "contextforge:\n  fields: {address: http://gateway.test, sandbox: true}\n  mcp: {expose: [get_health]}\n", 0o600)
	process := &recordingProcess{}
	var warnings []string
	manager := configManager(t, path, process, &warnings)
	if err := manager.Load(context.Background(), "contextforge"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg := process.initParams.Config
	if cfg["address"] != "http://gateway.test" || cfg["sandbox"] != "true" || cfg["token"] != "jwt-value" {
		t.Fatalf("Init config = %v, want the fields beside the secret", keysOf(cfg))
	}
	settings, ok := manager.Settings("contextforge")
	if !ok || strings.Join(settings.Fields, ",") != "address,sandbox" || strings.Join(settings.Expose, ",") != "get_health" || settings.SHA256 == "" {
		t.Fatalf("Settings = %+v, %v", settings, ok)
	}
}

// A refused entry refuses the load before the subprocess starts, says why,
// and a fixed file loads on the next try — which is also how a reload picks
// up an edit.
func TestManagerRefusesLoadOnConfigProblemsAndRecovers(t *testing.T) {
	path := writeConnectorConfig(t, "contextforge:\n  fields: {server: https://prod.example.test}\n", 0o600)
	process := &recordingProcess{}
	var warnings []string
	manager := configManager(t, path, process, &warnings)

	err := manager.Load(context.Background(), "contextforge")
	if err == nil || !strings.Contains(err.Error(), `field "server" is not declared`) {
		t.Fatalf("Load = %v, want the refusal", err)
	}
	if manager.Loaded("contextforge") || process.initParams.Config != nil {
		t.Fatal("a refused plugin was started")
	}
	if problems := manager.ConfigProblems("contextforge"); len(problems) != 1 {
		t.Fatalf("ConfigProblems = %v", problems)
	}

	if err := os.WriteFile(path, []byte("contextforge:\n  fields: {address: https://gw.example.test}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Load(context.Background(), "contextforge"); err != nil {
		t.Fatalf("Load after the fix: %v", err)
	}
	if process.initParams.Config["address"] != "https://gw.example.test" {
		t.Fatalf("the edited value did not reach the plugin: %v", process.initParams.Config)
	}
	if problems := manager.ConfigProblems("contextforge"); len(problems) != 0 {
		t.Fatalf("problems not cleared after a clean load: %v", problems)
	}
}

// An unreadable file refuses every load rather than being treated as absent.
func TestManagerRefusesLoadOnUnparseableConfig(t *testing.T) {
	path := writeConnectorConfig(t, "contextforge: [not, a, mapping\n", 0o600)
	var warnings []string
	manager := configManager(t, path, &recordingProcess{}, &warnings)
	err := manager.Load(context.Background(), "contextforge")
	if err == nil || !strings.Contains(err.Error(), "is unreadable") {
		t.Fatalf("Load = %v, want an unreadable-file refusal", err)
	}
}
