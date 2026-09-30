package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// The circuit breaker (§12, P5-c): an agent session policy refused too
// often is suspended until a person resets it here or on the console.

var breakerCmd = &cobra.Command{
	Use:   "breaker",
	Short: "List and reset the agent sessions the circuit breaker suspended",
	Long: `The circuit breaker suspends an agent session after repeated real policy
denials (policy's circuit_breaker: {denials, window}). A suspended session's
every call but a plain read is refused as session_suspended, in every
enforcement mode, until a person resets it. There is no automatic reset.`,
}

var breakerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List suspended sessions",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newBrakesClient()
		if err != nil {
			return err
		}
		view, err := client.Brakes(cmd.Context())
		if err != nil {
			return err
		}
		return writeSuspensions(cmd.OutOrStdout(), view.State)
	},
}

var breakerResetCmd = &cobra.Command{
	Use:   "reset <suspension-id>",
	Short: "Reset a suspended session (interactive, with a typed phrase)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		if !policyIsTerminal() {
			return errors.New("a suspended session is reset only from an interactive terminal, by a person, with a typed phrase (or on the console)")
		}
		out := cmd.OutOrStdout()
		phrase := "reset " + id
		fmt.Fprintf(out, "Type %q to reset it: ", phrase)
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		typed := strings.TrimSpace(line)
		if typed != phrase {
			return errors.New("the confirmation did not match; nothing was reset")
		}
		var st brake.State
		client, err := newBrakesClient()
		if err == nil {
			var view cerbapi.BrakesView
			if view, err = client.ResetSuspension(cmd.Context(), id, cerbapi.BrakeResetArgs{Typed: typed}); err == nil {
				st = view.State
			}
		}
		var unreachable *cerbapi.DaemonUnreachableError
		if err != nil && errors.As(err, &unreachable) {
			// No passkey is involved, so the store is reset here with the
			// daemon down, as the daemon would.
			store, serr := brakesStore()
			if serr != nil {
				return serr
			}
			st, err = cerbapi.ResetSuspension(inProcessContext(cmd.Context()), app.AuditSink(), store, id, typed)
		}
		if errors.Is(err, brake.ErrNotEngaged) {
			return fmt.Errorf("no suspension %s; `cerberus breaker list` shows them", id)
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "Reset.")
		return writeSuspensions(out, st)
	},
}

func writeSuspensions(w io.Writer, st brake.State) error {
	if len(st.Suspensions) == 0 {
		_, err := fmt.Fprintln(w, "No session is suspended.")
		return err
	}
	for _, x := range st.Suspensions {
		fmt.Fprintf(w, "SUSPENDED %s: %s over %s (%s) since %s, after %d policy denials in %s\n  reset: cerberus breaker reset %s\n",
			x.ID, x.Principal.Kind, x.Principal.Via, x.Key, x.TrippedAt.Local().Format(time.RFC3339), x.Denials, x.Window, x.ID)
	}
	return nil
}

func init() {
	breakerCmd.GroupID = "runtime"
	breakerCmd.AddCommand(breakerListCmd, breakerResetCmd)
	rootCmd.AddCommand(breakerCmd)
}
