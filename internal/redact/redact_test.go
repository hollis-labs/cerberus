package redact

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDiagnosticsRemoveCredentials(t *testing.T) {
	cases := []string{
		"API_KEY=sentinel-secret", "TOKEN: sentinel-secret", "SECRET => sentinel-secret", "PASSWORD='sentinel-secret'", "PRIVATE_KEY=sentinel-secret", "credentials: sentinel-secret", `{"api_key":"sentinel-secret"}`, "Authorization: Bearer sentinel-secret", "https://user:sentinel-secret@example.com/healthy", "--api-key sentinel-secret", "--token=sentinel-secret",
		"-----BEGIN PRIVATE KEY-----\nsentinel-secret\n-----END PRIVATE KEY-----",
		"<key>PASSWORD</key><string>sentinel-secret</string>",
		"<key>ProgramArguments</key><array><string>server</string><string>--token</string><string>sentinel-secret</string></array>",
		"arguments = {\n  0 = server\n  1 = --token\n  2 = sentinel-secret\n}\nstate = running",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			out := Launchd(input)
			if strings.Contains(out, "sentinel-secret") {
				t.Fatalf("credential survived: %s", out)
			}
			if !strings.Contains(out, Marker) {
				t.Fatal("missing replacement")
			}
		})
	}
	for _, token := range []string{"sk-proj-abcdefghijklmnop123456", "ghp_abcdefghijklmnop123456", "github_pat_abcdefghijklmnop123456", "xoxb-123456789012", "AKIA1234567890123456"} {
		if got := Text("received " + token); strings.Contains(got, token) {
			t.Fatal("provider token survived")
		}
	}
}

func TestJSONPreservesUsefulDataAndReferences(t *testing.T) {
	input := map[string]any{
		"env":           map[string]any{"API_KEY": "sentinel-env", "PASSWORD": 1234, "REGION": "us-east", "SECRET": "helper://vault/item/field"}, //nolint:gosec // synthetic redaction inputs
		"command":       []string{"server", "--token", "sentinel-arg", "--port", "9012"},
		"config_schema": map[string]any{"properties": map[string]any{"api_key": map[string]any{"type": "string", "description": "API key reference"}}},
		"secrets":       []any{map[string]any{"name": "api_key", "description": "API key", "required": true}},
		"count":         json.Number("9007199254740993"),
	}
	data, err := Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"sentinel-env", "sentinel-arg", "1234"} {
		if strings.Contains(string(data), value) {
			t.Fatal("credential survived structured response")
		}
	}
	for _, value := range []string{"helper://vault/item/field", "us-east", "9012", "9007199254740993", "API key reference"} {
		if !strings.Contains(string(data), value) {
			t.Fatalf("lost useful value %q: %s", value, data)
		}
	}
	var parsed map[string]any
	if err = json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	schema := parsed["config_schema"].(map[string]any)["properties"].(map[string]any)["api_key"].(map[string]any)
	if schema["type"] != "string" {
		t.Fatal("schema was redacted")
	}
	if parsed["secrets"].([]any)[0].(map[string]any)["name"] != "api_key" {
		t.Fatal("credential descriptor was redacted")
	}
	if input["env"].(map[string]any)["API_KEY"] != "sentinel-env" {
		t.Fatal("input was mutated")
	}
}

func TestKnownValuesAndErrors(t *testing.T) {
	r := FromEnv([]string{"API_KEY=opaque-sentinel", "REGION=us-east", "SECRET=keychain://provider/item"})
	if got := r.Text("remote echoed opaque-sentinel in us-east"); got != "remote echoed "+Marker+" in us-east" {
		t.Fatal(got)
	}
	source := errors.New("opaque-sentinel")
	safe := r.Error(source)
	if !errors.Is(safe, source) || safe.Error() != Marker {
		t.Fatal("redaction lost error identity or leaked value")
	}
	if Text("API_KEY=keychain://provider/item") != "API_KEY=keychain://provider/item" {
		t.Fatal("reference was hidden")
	}
}
