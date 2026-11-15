package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// whoami against main's output (testdata/golden-cli/whoami.{txt,json}: main's binary, scratch
// HOME, no daemon, stdin and stdout not a terminal; the uid is <uid>). Three lines differ on
// purpose: the explanation (the server classifies by the credential, not the terminal), the
// server's view named as the serving Cerberus, and with the server up an identity line with the
// key's scopes.

func notTerminal() cliClassification {
	c := localClassification()
	c.StdinTTY, c.StdoutTTY, c.Marker = false, false, ""
	return c
}

func baseline() policy.PostureSummary {
	return policy.PostureSummary{Global: policy.PostureSecure, Snapshot: policy.SnapshotBaseline,
		BreakGlass: policy.BreakGlass{PerTarget: 3, Window: 86400000000000}, Enforcement: "shadow (only the built-in protections are enforced)"}
}

func TestWhoamiWithTheServerDownMatchesMain(t *testing.T) {
	down := errors.New("the daemon is not running")
	cmds := func() []*cobra.Command {
		return []*cobra.Command{whoamiCommand(func() (*apiClient, error) { return nil, down }, notTerminal, baseline)}
	}
	uid := strconv.Itoa(notTerminal().Principal.UID)
	out, _, err := runCLI(t, cmds(), "", "whoami")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(golden(t, "whoami.txt"), "<uid>", uid)
	want = strings.Replace(want, "stdin or stdout is not a terminal, so this is treated as an agent (Decision 9)", cliExplanation, 1)
	want = strings.Replace(want, "The daemon's view: unavailable", "The serving Cerberus's view: unavailable", 1)
	if out != want {
		t.Errorf("text:\n got:\n%s\nwant (main's, explanation and server line as this build says them):\n%s", out, want)
	}
	out, _, err = runCLI(t, cmds(), "", "whoami", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	want = strings.ReplaceAll(golden(t, "whoami.json"), "<uid>", uid)
	want = strings.Replace(want, "stdin or stdout is not a terminal, so this is treated as an agent (Decision 9)", cliExplanation, 1)
	if out != want {
		t.Errorf("json:\n got:\n%s\nwant (main's, explanation as this build says it):\n%s", out, want)
	}
}

// With the server up, whoami shows the server's view of the CLI's key and its scopes.
func TestWhoamiShowsTheServersViewAndScopes(t *testing.T) {
	s := newBrakeServer(t, true)
	*s.as = libauth.Principal{Verified: true, ID: "key:k1", Kind: libauth.KindService, Display: api.CLIKeyName, Scopes: []string{api.ScopeRead}}
	cmds := []*cobra.Command{whoamiCommand(func() (*apiClient, error) { return s.c, nil }, notTerminal, baseline)}
	out, _, err := runCLI(t, cmds, "", "whoami")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"The serving Cerberus sees: agent (via cli, client cerberus-cli), verified by the identity module\n",
		"  identity: key:k1 (service cerberus-cli), scopes: cerberus.read\n",
		"Posture: secure\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("whoami lacks %q:\n%s", want, out)
		}
	}
	out, _, err = runCLI(t, cmds, "", "whoami", "-o", "json")
	if err != nil || !strings.Contains(out, `"daemon": {`) || !strings.Contains(out, `"scopes": [`) || strings.Contains(out, "daemon_note") {
		t.Fatalf("json: %s %v", out, err)
	}
	_ = gate.ViaCLI
}
