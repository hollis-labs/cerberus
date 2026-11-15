package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// `cerberus whoami` (slice 7d): old's verb, flags and output shape (cmd_whoami.go on main) over
// GET /api/whoami. Old classified the CLI itself, from its terminal and the CERBERUS_PRINCIPAL
// marker, and showed the daemon's view beside it. Here the serving Cerberus classifies a call by
// its credential: this CLI's key (cerberus-cli) is an agent via cli whatever its terminal
// (decision 0.1), so the local classification says that, and the server's view adds what the
// identity module verified, with the key's scopes.

// principalEnv is old's marker. It is shown, as old showed it; it no longer changes anything.
const principalEnv = "CERBERUS_PRINCIPAL"

// cliExplanation is why this CLI is classified as it is.
const cliExplanation = "the serving Cerberus classifies a call by the key it is made with, and this CLI's key (" + api.CLIKeyName +
	") is an agent via cli, whatever its terminal (decision 0.1); this is a label for default policy, never approval"

// cliClassification is old's local classification, in its JSON shape.
type cliClassification struct {
	Principal   gate.Principal `json:"principal"`
	StdinTTY    bool           `json:"stdin_tty"`
	StdoutTTY   bool           `json:"stdout_tty"`
	Marker      string         `json:"cerberus_principal,omitempty"`
	Explanation string         `json:"explanation"`
}

// whoamiReport is old's report, plus the identity the server verified.
type whoamiReport struct {
	Local      cliClassification   `json:"local"`
	Daemon     *gate.Principal     `json:"daemon,omitempty"`
	DaemonNote string              `json:"daemon_note,omitempty"`
	Identity   *api.WhoAmIIdentity `json:"identity,omitempty"`
	// Posture is the applied posture a call from here is evaluated under.
	Posture policy.PostureSummary `json:"posture"`
}

// localClassification is how the serving Cerberus labels this CLI's calls, with old's terminal
// facts beside it.
func localClassification() cliClassification {
	return cliClassification{
		Principal: gate.Principal{Kind: gate.PrincipalAgent, Via: gate.ViaCLI, UID: os.Getuid(), UIDVerified: true, Client: api.CLIKeyName, SelfReported: true},
		StdinTTY:  term.IsTerminal(int(os.Stdin.Fd())), StdoutTTY: term.IsTerminal(int(os.Stdout.Fd())), //nolint:gosec // a file descriptor fits an int
		Marker:      os.Getenv(principalEnv),
		Explanation: cliExplanation,
	}
}

// posture, with the server down, is the applied posture read from the policy files, as old read it.
func whoamiCommand(client func() (*apiClient, error), local func() cliClassification, posture func() policy.PostureSummary) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show how Cerberus classifies the current caller",
		Long: `Show how Cerberus classifies this caller: human, agent or automation, and
why, and what the serving Cerberus sees: the principal its gate records, the
identity it verified, and that credential's scopes.

The serving Cerberus classifies a call by its credential. This CLI calls with
its API key (cerberus-cli), an agent via cli whatever its terminal; only the
owner signed in to the console is a person (decision 0.1).

The classification is a label that picks default policy. It is never
approval, and nothing depends on it being unforgeable.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := whoamiReport{Local: local()}
			cl, err := client()
			if err == nil {
				var seen api.WhoAmI
				if err = cl.get(cmd.Context(), "/whoami", &seen); err == nil {
					out.Daemon, out.Identity, out.Posture = &seen.Principal, &seen.Identity, seen.Posture
				}
			}
			if err != nil {
				out.DaemonNote = strings.TrimSpace(err.Error())
				out.Posture = posture()
			}
			if output == outputJSON {
				return printJSON(cmd.OutOrStdout(), out)
			}
			return writeWhoami(cmd.OutOrStdout(), out)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "output format: text|json")
	return cmd
}

// writeWhoami is old's text, the server's view named as the serving Cerberus, with the
// identity and its scopes.
func writeWhoami(w io.Writer, r whoamiReport) error {
	p := r.Local.Principal
	marker := r.Local.Marker
	if marker == "" {
		marker = "(unset)"
	}
	_, err := fmt.Fprintf(w, "Classified as: %s (via %s, uid %d)\n  stdin TTY: %t, stdout TTY: %t, %s: %s\n  %s\n",
		p.Kind, p.Via, p.UID, r.Local.StdinTTY, r.Local.StdoutTTY, principalEnv, marker, r.Local.Explanation)
	if err != nil {
		return err
	}
	switch {
	case r.Daemon != nil:
		d := r.Daemon
		how := "verified by the identity module"
		if d.SelfReported {
			how = "self-reported"
		}
		_, err = fmt.Fprintf(w, "The serving Cerberus sees: %s (via %s, client %s), %s\n", d.Kind, d.Via, d.Client, how)
		if err == nil && r.Identity != nil {
			scopes := "none"
			if len(r.Identity.Scopes) > 0 {
				scopes = strings.Join(r.Identity.Scopes, ", ")
			}
			name := ""
			if r.Identity.Display != "" {
				name = " " + r.Identity.Display
			}
			_, err = fmt.Fprintf(w, "  identity: %s (%s%s), scopes: %s\n", r.Identity.ID, r.Identity.Kind, name, scopes)
		}
	case r.DaemonNote != "":
		_, err = fmt.Fprintf(w, "The serving Cerberus's view: unavailable (%s)\n", r.DaemonNote)
	}
	if err != nil {
		return err
	}
	posture := r.Posture.String()
	if r.Posture.Global == "" {
		posture = policy.PostureSecure
	}
	_, err = fmt.Fprintf(w, "Posture: %s\nThis is a label for default policy, never approval.\n", posture)
	return err
}

func (c *cerberus) whoamiCommand() *cobra.Command {
	return whoamiCommand(func() (*apiClient, error) { return newAPIClient(c.app, c.home) }, localClassification,
		func() policy.PostureSummary { return policy.Store{Dir: policyDir(c.home)}.CurrentPosture() })
}
