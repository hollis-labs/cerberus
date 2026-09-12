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
