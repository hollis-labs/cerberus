package mcp

import (
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// A suspended or braked agent is told to stop and ask a person, and the
// guidance survives redaction.
func TestNextStepForBrakesAndTheBreaker(t *testing.T) {
	for _, code := range []cerbapi.ExternalConnectorErrorCode{cerbapi.ExternalConnectorSessionSuspended, cerbapi.ExternalConnectorLockdown, cerbapi.ExternalConnectorFrozen} {
		step := nextStep(code, nil)
		if step == nil || !strings.Contains(step.Error(), "stop") || !strings.Contains(step.Error(), "Only a person") {
			t.Fatalf("%s: %v", code, step)
		}
		if redact.Text(step.Error()) != step.Error() {
			t.Fatalf("%s: redaction rewrote %q", code, step.Error())
		}
	}
}

// mcp-http's scope check sees an SFTP download into a local path as a
// write, so a read_sensitive token is refused before it is forwarded (H2).
func TestLocalWritesNeedOperateAtTheEdge(t *testing.T) {
	effect, ok := ToolEffect("cerberus_ssh_get")
	if !ok || effect != "write" {
		t.Fatalf("cerberus_ssh_get reads as %q", effect)
	}
	if effect, _ := ToolEffect("cerberus_ssh_exec"); effect != "exec" {
		t.Fatalf("cerberus_ssh_exec reads as %q", effect)
	}
}
