package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPResultsRedactDiagnosticSecrets(t *testing.T) {
	data := marshalResult(lifecycleResult{Success: false, Error: "API_KEY=mcp-sentinel", BuildOutput: "Authorization: Bearer build-sentinel"})
	if !json.Valid([]byte(data)) {
		t.Fatal("invalid JSON")
	}
	if strings.Contains(data, "mcp-sentinel") || strings.Contains(data, "build-sentinel") {
		t.Fatal("credential leaked")
	}
	logs, err := marshalConnectorData("TOKEN=log-sentinel")
	if err != nil || strings.Contains(logs, "log-sentinel") {
		t.Fatal("connector logs leaked")
	}
}

// An operation's result reaches the model through marshalConnectorData, and
// never gets the declared-schema exemption, however it is shaped: a plugin
// result built to look like the credential editor's DTO, or a definition,
// comes out with its names and envs hidden, as on main.
func TestConnectorDataShapedLikeOurSchemaIsNotExempt(t *testing.T) {
	const name, env = "ghp_nameShapedToken12345", "VENDOR_ENV_SHAPED_TOKEN_67890"
	for label, data := range map[string]any{
		"credentials DTO": map[string]any{"providers": []any{map[string]any{"id": "x", "secrets": []any{map[string]any{"name": name, "env": env}}}}},
		"definition":      map[string]any{"id": "x", "operations": []any{}, "config": map[string]any{"secrets": []any{map[string]any{"name": name, "env": env}}}},
		"definition list": []any{map[string]any{"id": "x", "operations": []any{}, "config": map[string]any{"secrets": []any{map[string]any{"name": name, "env": env}}}}},
	} {
		out, err := marshalConnectorData(data)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, name) || strings.Contains(out, env) {
			t.Errorf("%s: operation data got the exemption: %s", label, out)
		}
	}
}
