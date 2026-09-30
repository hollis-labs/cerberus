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
