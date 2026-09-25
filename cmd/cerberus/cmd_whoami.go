package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/policy"
)

var whoamiOutput string

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show how Cerberus classifies the current caller",
	Long: `Show how Cerberus classifies this caller: human, agent or automation, and
why. A CLI call is human only from an interactive terminal (stdin and stdout
both TTYs) with no CERBERUS_PRINCIPAL=agent marker; anything else is an agent.
Agent launchers set that marker, so this is where to check one did.

When the daemon is running, its view is shown too: the same claim, marked
self-reported, with the uid it read from the socket's peer credentials.

The classification is a label that picks default policy. It is never
approval, and nothing depends on it being unforgeable.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		local := cerbapi.DetectCLI()
		out := whoamiReport{Local: local, Posture: currentPosture()}
		if client, err := newResourceSocketClient(); err == nil {
			daemon, askErr := client.WhoAmI(cmd.Context())
			var unreachable *cerbapi.DaemonUnreachableError
			switch {
			case askErr == nil:
				out.Daemon = &daemon
			case errors.As(askErr, &unreachable):
				out.DaemonNote = "the daemon is not running"
			default:
				out.DaemonNote = strings.TrimSpace(askErr.Error())
			}
		}
		if whoamiOutput == outputFormatJSON {
			return printJSON(out)
		}
		return writeWhoami(cmd.OutOrStdout(), out)
	},
}

type whoamiReport struct {
	Local      cerbapi.CLIClassification `json:"local"`
	Daemon     *cerbapi.Principal        `json:"daemon,omitempty"`
	DaemonNote string                    `json:"daemon_note,omitempty"`
	// Posture is the applied posture a call from here is evaluated under.
	Posture policy.PostureSummary `json:"posture"`
}

func writeWhoami(w io.Writer, r whoamiReport) error {
	p := r.Local.Principal
	marker := r.Local.Marker
	if marker == "" {
		marker = "(unset)"
	}
	_, err := fmt.Fprintf(w, "Classified as: %s (via %s, uid %d)\n  stdin TTY: %t, stdout TTY: %t, %s: %s\n  %s\n",
		p.Kind, p.Via, p.UID, r.Local.StdinTTY, r.Local.StdoutTTY, cerbapi.PrincipalEnv, marker, r.Local.Explanation)
	if err != nil {
		return err
	}
	switch {
	case r.Daemon != nil:
		d := r.Daemon
		verified := "unverified"
		if d.UIDVerified {
			verified = "verified from peer credentials"
		}
		_, err = fmt.Fprintf(w, "The daemon sees: %s (via %s, client %s), uid %d %s; kind and client are self-reported\n", d.Kind, d.Via, d.Client, d.UID, verified)
	case r.DaemonNote != "":
		_, err = fmt.Fprintf(w, "The daemon's view: unavailable (%s)\n", r.DaemonNote)
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

func init() {
	whoamiCmd.GroupID = "runtime"
	addOutputFlag(whoamiCmd, &whoamiOutput)
	rootCmd.AddCommand(whoamiCmd)
}
