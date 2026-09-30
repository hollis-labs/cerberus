package main

import (
	"bufio"
	"context"
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

// The emergency brake (§12). Engaging is one command anyone can run, with
// or without the daemon. Lifting is a person's act: on a terminal, with a
// typed phrase, and with a passkey on the console where one is enrolled.

var brakeFlags struct {
	reason   string
	off      bool
	approval string
	scope    []string
}

// brakesClient is the daemon's brakes, as these commands use them. Tests
// swap newBrakesClient.
type brakesClient interface {
	Brakes(ctx context.Context) (cerbapi.BrakesView, error)
	EngageBrake(ctx context.Context, args cerbapi.BrakeEngageArgs) (cerbapi.BrakesView, error)
	LiftBrake(ctx context.Context, freezeID string, args cerbapi.BrakeLiftArgs) (cerbapi.BrakesView, error)
}

var newBrakesClient = func() (brakesClient, error) { return newResourceSocketClient() }

// brakesStore is the local brake store, for engaging with no daemon.
var brakesStore = func() (brake.Store, error) {
	dir, err := app.BrakesDir()
	return brake.Store{Dir: dir}, err
}

var lockdownCmd = &cobra.Command{
	Use:   "lockdown",
	Short: "Put Cerberus in lockdown: only plain reads run (--off lifts it)",
	Long: `Lockdown is the emergency brake: every operation but a plain read is refused,
in every enforcement mode, until a person lifts it. It survives a daemon restart.
Engaging it takes one command and no confirmation, from here, the console or an
agent's MCP tool, and it works with the daemon down.

Lifting it (--off) is a person's act on a terminal, with a typed phrase. Where a
passkey is enrolled, it is also approved with the passkey on the console: the
first --off prints the link, and --off --approval <id> lifts it once approved.

Resource auto-restarts keep running under a lockdown, to keep declared services
up; pipelines do not run.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if brakeFlags.off {
			return liftBrake(cmd, "", "lift lockdown")
		}
		return engageBrake(cmd, cerbapi.BrakeEngageArgs{Reason: brakeFlags.reason})
	},
}

var freezeCmd = &cobra.Command{
	Use:   "freeze --scope key=value... | --off <freeze-id> | list",
	Short: "Freeze the targets a scope selects: only plain reads run on them",
	Long: `A freeze is a lockdown for the targets it matches: every operation but a plain
read on them is refused, their auto-restarts pause and pipelines touching them
do not run. --scope takes id, connector, kind, env, owner, admin or tag, as
key=value, repeatable. Engaging is one command; lifting (--off <id>) is a
person's act, like lifting a lockdown.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) == 1 && args[0] == "list":
			return listBrakes(cmd)
		case brakeFlags.off:
			if len(args) != 1 {
				return errors.New("name the freeze to lift: cerberus freeze --off <freeze-id> (cerberus freeze list shows them)")
			}
			return liftBrake(cmd, args[0], "lift "+args[0])
		}
		match, err := parseScope(brakeFlags.scope)
		if err != nil {
			return err
		}
		if match == nil {
			return errors.New("name what to freeze with --scope key=value, for example --scope env=prod or --scope id=payments-db")
		}
		return engageBrake(cmd, cerbapi.BrakeEngageArgs{Reason: brakeFlags.reason, Match: match})
	},
}

func engageBrake(cmd *cobra.Command, args cerbapi.BrakeEngageArgs) error {
	out := cmd.OutOrStdout()
	var st brake.State
	client, err := newBrakesClient()
	if err == nil {
		var view cerbapi.BrakesView
		if view, err = client.EngageBrake(cmd.Context(), args); err == nil {
			st = view.State
		}
	}
	var unreachable *cerbapi.DaemonUnreachableError
	if err != nil && errors.As(err, &unreachable) {
		// Engaging must work with the daemon down: write the store here.
		store, serr := brakesStore()
		if serr != nil {
			return serr
		}
		ctx := inProcessContext(cmd.Context())
		if args.Match != nil {
			st, _, err = cerbapi.EngageFreeze(ctx, app.AuditSink(), store, *args.Match, args.Reason)
		} else {
			st, _, err = cerbapi.EngageLockdown(ctx, app.AuditSink(), store, args.Reason)
		}
		if err == nil {
			fmt.Fprintln(out, "(the daemon is not running; engaged in the brake store, which it reads when it starts)")
		}
	}
	if err != nil {
		return err
	}
	return writeBrakes(out, st)
}

func liftBrake(cmd *cobra.Command, freezeID, phrase string) error {
	if !policyIsTerminal() {
		return errors.New("a brake is lifted only from an interactive terminal, by a person, with a typed phrase; engaging needs none of that")
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Type %q to lift it: ", phrase)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(line) != phrase {
		return errors.New("the confirmation did not match; nothing was lifted")
	}
	client, err := newBrakesClient()
	if err != nil {
		return err
	}
	view, err := client.LiftBrake(inProcessContext(cmd.Context()), freezeID, cerbapi.BrakeLiftArgs{ApprovalID: brakeFlags.approval})
	var unreachable *cerbapi.DaemonUnreachableError
	if errors.As(err, &unreachable) {
		// Never lifted in-process: the passkey check lives in the daemon, and
		// a lift without it would fall to the terminal floor.
		return errors.New("lifting a brake needs the daemon, which checks for an enrolled passkey; start it with `cerberus daemon start`, then retry")
	}
	var coded *cerbapi.ExternalConnectorError
	if errors.As(err, &coded) && coded.Approval != nil && coded.Code == cerbapi.ExternalConnectorApprovalPending {
		// A passkey is enrolled: the lift is approved with it on the console.
		fmt.Fprintln(out, err.Error())
		return printConsoleLink(out, "/approvals?id="+coded.Approval.ID, false, "Approve it with your passkey on the console, then run this again with --approval "+coded.Approval.ID+":")
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Lifted.")
	return writeBrakes(out, view.State)
}

func listBrakes(cmd *cobra.Command) error {
	client, err := newBrakesClient()
	if err != nil {
		return err
	}
	view, err := client.Brakes(cmd.Context())
	if err != nil {
		return err
	}
	for _, p := range view.Problems {
		fmt.Fprintf(cmd.OutOrStdout(), "Warning: %s\n", p)
	}
	return writeBrakes(cmd.OutOrStdout(), view.State)
}

func writeBrakes(w io.Writer, st brake.State) error {
	if !st.Engaged() {
		_, err := fmt.Fprintln(w, "No brake is engaged.")
		return err
	}
	if l := st.Lockdown; l != nil {
		fmt.Fprintf(w, "LOCKDOWN %s since %s, by %s over %s%s\n  lift: cerberus lockdown --off\n", l.ID, l.EngagedAt.Local().Format(time.RFC3339), l.By.Kind, l.By.Via, brakeReason(l.Reason))
	}
	for _, f := range st.Freezes {
		fmt.Fprintf(w, "FREEZE %s on %s since %s, by %s over %s%s\n  lift: cerberus freeze --off %s\n", f.ID, f.Match.String(), f.EngagedAt.Local().Format(time.RFC3339), f.By.Kind, f.By.Via, brakeReason(f.Reason), f.ID)
	}
	return nil
}

func brakeReason(r string) string {
	if r == "" {
		return ""
	}
	return ": " + r
}

func init() {
	for _, c := range []*cobra.Command{lockdownCmd, freezeCmd} {
		c.GroupID = "runtime"
		c.Flags().StringVar(&brakeFlags.reason, "reason", "", "why, for the record and the banner")
		c.Flags().BoolVar(&brakeFlags.off, "off", false, "lift it (a person, on a terminal; with a passkey where one is enrolled)")
		c.Flags().StringVar(&brakeFlags.approval, "approval", "", "the approved passkey approval that lifts it")
	}
	freezeCmd.Flags().StringArrayVar(&brakeFlags.scope, "scope", nil, "what to freeze, as key=value (repeatable)")
	rootCmd.AddCommand(lockdownCmd, freezeCmd)
}
