package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/policy"
)

var scopedPermissive = policy.File{PostureRules: []policy.PostureRule{{Match: policy.TargetMatch{Env: "dev"}, Posture: policy.PosturePermissive}}}.PostureSummary("h")

// whoami, status and the MCP instructions all name the applied posture, so
// neither the operator nor an agent can mistake a permissive install for a
// secure one (section 13).
func TestPostureIsVisibleOnEverySurface(t *testing.T) {
	var out bytes.Buffer
	if err := writeWhoami(&out, whoamiReport{Local: cerbapi.ClassifyCLI(true, true, ""), Posture: scopedPermissive}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Posture: secure; permissive for env=dev (labeled targets only)") {
		t.Fatalf("whoami:\n%s", out.String())
	}

	out.Reset()
	if err := writeStatus(&out, statusReport{Posture: scopedPermissive}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "posture  secure; permissive for env=dev") {
		t.Fatalf("status:\n%s", out.String())
	}

	secure := mcp.Instructions(policy.PostureSummary{Global: policy.PostureSecure})
	open := mcp.Instructions(policy.File{Posture: policy.PosturePermissive}.PostureSummary("h"))
	if !strings.Contains(secure, "Posture: secure.") || strings.Contains(secure, "permissive") {
		t.Fatalf("secure instructions: %s", secure)
	}
	if !strings.Contains(open, "Posture: permissive.") || !strings.Contains(open, "still recorded in the audit log") {
		t.Fatalf("permissive instructions: %s", open)
	}
}
