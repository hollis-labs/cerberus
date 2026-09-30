package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
)

// approvals show and approve print what the call would run, flag the
// requester's text, and keep it inert on a terminal (H3).
func TestApprovalsShowWhatTheCallWouldRun(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"host": "db1", "command": "rm -rf /srv/data \x1b[2J restart nginx"})
	a := approval.Approval{ID: "a1", Status: approval.Pending, Connector: "ssh", Operation: "exec", Effect: "exec", Channel: approval.ChannelOutOfBand, Scope: approval.ScopeOnce,
		PlanHash: "sha256:abc", Shown: &approval.Shown{
			Plan:      json.RawMessage(`{"connector":"ssh","operation":"exec","preview":{"command":"rm -rf /srv/data"}}`),
			Arguments: args,
			Untrusted: []string{"/plan/preview", "/arguments"},
		}}
	var out bytes.Buffer
	if err := writeApproval(&out, a, "the daemon"); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Plan it binds to:", "Arguments:", "rm -rf /srv/data", "written by the requester, not Cerberus"} {
		if !strings.Contains(text, want) {
			t.Errorf("show lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b") {
		t.Fatal("a raw escape sequence from the requester reached the terminal")
	}

	a.Shown = nil
	out.Reset()
	_ = writeApproval(&out, a, "the daemon")
	if !strings.Contains(out.String(), "not recorded with this approval") {
		t.Fatalf("an approval with nothing stored does not say so:\n%s", out.String())
	}
}
