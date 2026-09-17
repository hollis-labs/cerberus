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

// A safety net that eats the instruction is worse than no instruction: the
// ContextForge plugin's 401 guidance came back as "accepts a Bearer [REDACTED]
// only", which told the operator nothing about what to do.
func TestTextKeepsBearerGuidanceReadable(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"ContextForge accepts a Bearer JWT only", "ContextForge accepts a Bearer JWT only"},
		{"use Bearer auth", "use Bearer auth"},
		{"the gateway wants a Bearer token", "the gateway wants a Bearer token"},
		{"supply Bearer credentials", "supply Bearer credentials"},
	} {
		if got := Text(tt.in); got != tt.want {
			t.Errorf("Text(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Loosening the rule must not let a real credential through.
func TestTextStillRedactsBearerTokens(t *testing.T) {
	for _, in := range []string{
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc-def",
		"Bearer sk-ant-0123456789abcdef0123",
		"Bearer abc123",                   // short, but not a word
		"Bearer a1b2c3",                   // digits mixed in
		"Bearer abcdefghijklmnopqrstuvwx", // 24 letters: long enough to be a token
		"bearer CAFEBABEDEADBEEF0123",
	} {
		got := Text(in)
		if !strings.Contains(got, Marker) {
			t.Errorf("Text(%q) = %q, expected redaction", in, got)
		}
	}
}

// missing_secrets answers "which credential is missing?". SensitiveKey matches
// it on the word SECRET, so before the names-only allow-list the answer came
// back as ["[REDACTED]"] and `managed list` could not tell an operator what to
// supply.
func TestMissingSecretsKeepsItsNames(t *testing.T) {
	data, err := Marshal(map[string]any{
		"id":              "contextforge",
		"loaded":          true,
		"missing_secrets": []string{"token", "api_key"},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{"token", "api_key"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing_secrets lost %q: %s", want, data)
		}
	}
	if strings.Contains(string(data), Marker) {
		t.Fatalf("a names-only field should not be redacted at all: %s", data)
	}
}

// The allow-list must stay an allow-list: a sibling key that really can carry a
// value is still redacted, and a names-only field nested under an already
// hidden parent stays hidden.
func TestNamesOnlyAllowListDoesNotWiden(t *testing.T) {
	data, err := Marshal(map[string]any{
		"missing_secrets": []string{"token"},
		"api_token":       "sk-live-0123456789abcdef",
		"credentials": map[string]any{
			"missing_secrets": []string{"nested"},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "sk-live-0123456789abcdef") {
		t.Fatalf("a real credential leaked: %s", data)
	}
	if strings.Contains(string(data), "nested") {
		t.Fatalf("an inherited hide must still win: %s", data)
	}
	// Anchored on the field, not the bare word: "token" also appears in the
	// api_token key name, which is never redacted.
	if !strings.Contains(string(data), `"missing_secrets":["token"]`) {
		t.Fatalf("the top-level names-only field was redacted: %s", data)
	}
}

func TestNamesOnlyKey(t *testing.T) {
	if !NamesOnlyKey("missing_secrets") || !NamesOnlyKey("Missing_Secrets") {
		t.Fatal("missing_secrets should be names-only")
	}
	for _, key := range []string{"secrets", "api_token", "password", ""} {
		if NamesOnlyKey(key) {
			t.Fatalf("%q must not be treated as names-only", key)
		}
	}
}

// The sixth and seventh time redaction ate its own guidance, and the first two
// that were not the Bearer case. Both are recovery instructions: the word the
// rule removed is the word that told the operator what to do.
func TestTextKeepsRecoveryGuidanceReadable(t *testing.T) {
	for _, in := range []string{
		// `assignment` read the error code as a key, because
		// "credential_missing" contains "credential", and ate "reload".
		"contextforge list_gateways: credential_missing: reload the plugin after setting CONTEXTFORGE_JWT",
		"contextforge get_health: credential_missing: run `cerberus plugin managed list` to see which name is unset",
		// `flag` read the hyphen inside a header name as a flag dash, because
		// `--?` was satisfied by any internal hyphen, and ate "header".
		"gateway rejected the request; set X-API-Key header on the tunnel",
		"pass X-Auth-Token header when the gateway sits behind the proxy",
	} {
		if got := Text(in); got != in {
			t.Errorf("guidance was mangled:\n  in:  %s\n  got: %s", in, got)
		}
	}
}

// Neither exemption may become a way through. The error code is prose, but the
// text behind it is not exempt, and a header name with an actual value is a
// real leak that the assignment rule still has to catch.
func TestRecoveryGuidanceExemptionsStillRedact(t *testing.T) {
	for _, in := range []string{
		"credential_missing: API_KEY=sentinel-secret",
		"credential_missing: token=sentinel-secret",
		"X-API-Key: sentinel-secret",
		"curl -H 'X-API-Key: sentinel-secret' https://example.com",
		"run with --api-key sentinel-secret",
		"cerberus ssh exec --token=sentinel-secret",
	} {
		got := Text(in)
		if strings.Contains(got, "sentinel-secret") {
			t.Errorf("credential survived: Text(%q) = %q", in, got)
		}
		if !strings.Contains(got, Marker) {
			t.Errorf("missing replacement: Text(%q) = %q", in, got)
		}
	}
}
