package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// The emergency brake (§12) and the circuit breaker on the CLI: the old `lockdown`, `freeze` and
// `breaker` verbs (cmd/cerberus cmd_lockdown.go and cmd_breaker.go on main), with the old
// flags and output, over the serving Cerberus's /api/brakes routes with the CLI's key.
//
// Who may do what is the server's: engaging needs a key holding cerberus.admin; a lift needs a
// person and the owner's approval on the console, and a reset a person typing "reset <id>". The
// only person the server knows is the owner signed in to the console; this CLI calls with an
// API key, which the gate treats as an agent, so the server refuses its lift and reset with
// 428 approval_required, and the owner does them from the console session.

// stdIsTerminal reports whether stdin and stdout are a terminal: a lift and a reset are typed
// by a person, as the old CLI required.
func stdIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) //nolint:gosec // a file descriptor fits an int
}

// brakeCommands are `lockdown`, `freeze` and `breaker`, against the server client returns.
func brakeCommands(client func() (*apiClient, error), isTerminal func() bool) []*cobra.Command {
	var reason, approvalID string
	var off bool
	var scope []string

	lockdown := &cobra.Command{
		Use:   "lockdown",
		Short: "Put Cerberus in lockdown: only plain reads run (--off lifts it)",
		Long: `Lockdown is the emergency brake: every operation but a plain read is refused,
in every enforcement mode, until a person lifts it. It survives a restart.
Engaging it takes one command and no confirmation, from here (a key holding
cerberus.admin) or the console.

Lifting it (--off) is a person's act, approved by the owner on the console: the
first --off asks for the approval, and --off --approval <id> lifts it once
approved. The server knows a person only as the owner signed in to the console;
this CLI calls with an API key, which is refused a lift.

Resource auto-restarts keep running under a lockdown, to keep declared services
up.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if off {
				return liftBrake(cmd, client, isTerminal, "", "lift lockdown", approvalID)
			}
			return engageBrake(cmd, client, gate.BrakeEngageArgs{Reason: reason})
		},
	}

	freeze := &cobra.Command{
		Use:   "freeze --scope key=value... | --off <freeze-id> | list",
		Short: "Freeze the targets a scope selects: only plain reads run on them",
		Long: `A freeze is a lockdown for the targets it matches: every operation but a plain
read on them is refused and their auto-restarts pause. --scope takes id,
connector, kind, env, owner, admin or tag, as key=value, repeatable. Engaging is
one command; lifting (--off <id>) is a person's act, like lifting a lockdown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case len(args) == 1 && args[0] == "list":
				return listBrakes(cmd, client)
			case off:
				if len(args) != 1 {
					return errors.New("name the freeze to lift: cerberus freeze --off <freeze-id> (cerberus freeze list shows them)")
				}
				return liftBrake(cmd, client, isTerminal, args[0], "lift "+args[0], approvalID)
			}
			match, err := parseScope(scope)
			if err != nil {
				return err
			}
			if match == nil {
				return errors.New("name what to freeze with --scope key=value, for example --scope env=prod or --scope id=payments-db")
			}
			return engageBrake(cmd, client, gate.BrakeEngageArgs{Reason: reason, Match: match})
		},
	}
	for _, c := range []*cobra.Command{lockdown, freeze} {
		c.Flags().StringVar(&reason, "reason", "", "why, for the record and the banner")
		c.Flags().BoolVar(&off, "off", false, "lift it (a person, with the owner's approval on the console)")
		c.Flags().StringVar(&approvalID, "approval", "", "the approved console approval that lifts it")
	}
	freeze.Flags().StringArrayVar(&scope, "scope", nil, "what to freeze, as key=value (repeatable)")

	breaker := &cobra.Command{
		Use:   "breaker",
		Short: "List and reset the agent sessions the circuit breaker suspended",
		Long: `The circuit breaker suspends an agent session after repeated real policy
denials (policy's circuit_breaker: {denials, window}). An API key is an agent,
named by its key. A suspended session's every call but a plain read is refused
as session_suspended, in every enforcement mode, until a person resets it.
There is no automatic reset.`,
	}
	breakerList := &cobra.Command{
		Use:   "list",
		Short: "List suspended sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			var view gate.BrakesView
			if err = cl.get(cmd.Context(), "/brakes", &view); err != nil {
				return err
			}
			return writeSuspensions(cmd.OutOrStdout(), view.State)
		},
	}
	breakerReset := &cobra.Command{
		Use:   "reset <suspension-id>",
		Short: "Reset a suspended session (interactive, with a typed phrase)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !isTerminal() {
				return errors.New("a suspended session is reset only from an interactive terminal, by a person, with a typed phrase (or on the console)")
			}
			out := cmd.OutOrStdout()
			phrase := "reset " + id
			typed, err := askPhrase(cmd, fmt.Sprintf("Type %q to reset it: ", phrase))
			if err != nil {
				return err
			}
			if typed != phrase {
				return errors.New("the confirmation did not match; nothing was reset")
			}
			cl, err := client()
			if err != nil {
				return err
			}
			var view gate.BrakesView
			err = cl.post(cmd.Context(), "/brakes/suspensions/"+url.PathEscape(id)+"/reset", gate.BrakeResetArgs{Typed: typed}, &view)
			var refused *apiError
			switch {
			case errors.As(err, &refused) && refused.Code == "not_engaged":
				return fmt.Errorf("no suspension %s; `cerberus breaker list` shows them", id)
			case err != nil:
				return personOnlyHint(err, cl, "a suspended session is reset")
			}
			_, _ = fmt.Fprintln(out, "Reset.")
			return writeSuspensions(out, view.State)
		},
	}
	breaker.AddCommand(breakerList, breakerReset)
	return []*cobra.Command{lockdown, freeze, breaker}
}

// askPhrase prints prompt and reads one line.
func askPhrase(cmd *cobra.Command, prompt string) (string, error) {
	_, _ = fmt.Fprint(cmd.OutOrStdout(), prompt)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// personOnlyHint adds, to the server's refusal of a lift or a reset from this CLI's key, where
// it is done instead.
func personOnlyHint(err error, cl *apiClient, what string) error {
	var refused *apiError
	if errors.As(err, &refused) && refused.Status == http.StatusPreconditionRequired && refused.Code == string(gate.CodeApprovalRequired) {
		return fmt.Errorf("%w\n%s by the owner signed in to the console; this CLI calls with the API key in %s, which the server treats as an agent", err, what, cl.keyFile)
	}
	return err
}

func engageBrake(cmd *cobra.Command, client func() (*apiClient, error), args gate.BrakeEngageArgs) error {
	cl, err := client()
	if err != nil {
		return err
	}
	path := "/brakes/lockdown"
	if args.Match != nil {
		path = "/brakes/freeze"
	}
	var view gate.BrakesView
	if err = cl.post(cmd.Context(), path, args, &view); err != nil {
		return err
	}
	return writeBrakes(cmd.OutOrStdout(), view.State)
}

func liftBrake(cmd *cobra.Command, client func() (*apiClient, error), isTerminal func() bool, freezeID, phrase, approvalID string) error {
	if !isTerminal() {
		return errors.New("a brake is lifted only from an interactive terminal, by a person, with a typed phrase; engaging needs none of that")
	}
	out := cmd.OutOrStdout()
	typed, err := askPhrase(cmd, fmt.Sprintf("Type %q to lift it: ", phrase))
	if err != nil {
		return err
	}
	if typed != phrase {
		return errors.New("the confirmation did not match; nothing was lifted")
	}
	cl, err := client()
	if err != nil {
		return err
	}
	path := "/brakes/lockdown/lift"
	if freezeID != "" {
		path = "/brakes/freeze/" + url.PathEscape(freezeID) + "/lift"
	}
	var view gate.BrakesView
	err = cl.post(cmd.Context(), path, gate.BrakeLiftArgs{ApprovalID: approvalID}, &view)
	var refused *apiError
	if errors.As(err, &refused) && refused.Code == string(gate.CodeApprovalPending) && refused.Approval != nil {
		// The owner approves the lift on the console; the retry naming it lifts.
		_, _ = fmt.Fprintln(out, refused.Message)
		_, _ = fmt.Fprintf(out, "Approve %s on the console, signed in as the owner, then run this again with --approval %s\n", refused.Approval.ID, refused.Approval.ID)
		return nil
	}
	if err != nil {
		return personOnlyHint(err, cl, "a brake is lifted")
	}
	_, _ = fmt.Fprintln(out, "Lifted.")
	return writeBrakes(out, view.State)
}

func listBrakes(cmd *cobra.Command, client func() (*apiClient, error)) error {
	cl, err := client()
	if err != nil {
		return err
	}
	var view gate.BrakesView
	if err = cl.get(cmd.Context(), "/brakes", &view); err != nil {
		return err
	}
	for _, p := range view.Problems {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Warning: %s\n", p)
	}
	return writeBrakes(cmd.OutOrStdout(), view.State)
}

// writeBrakes is the old output.
func writeBrakes(w io.Writer, st brake.State) error {
	if !st.Engaged() {
		_, err := fmt.Fprintln(w, "No brake is engaged.")
		return err
	}
	if l := st.Lockdown; l != nil {
		_, _ = fmt.Fprintf(w, "LOCKDOWN %s since %s, by %s over %s%s\n  lift: cerberus lockdown --off\n", l.ID, l.EngagedAt.Local().Format(time.RFC3339), l.By.Kind, l.By.Via, brakeReason(l.Reason))
	}
	for _, f := range st.Freezes {
		_, _ = fmt.Fprintf(w, "FREEZE %s on %s since %s, by %s over %s%s\n  lift: cerberus freeze --off %s\n", f.ID, f.Match.String(), f.EngagedAt.Local().Format(time.RFC3339), f.By.Kind, f.By.Via, brakeReason(f.Reason), f.ID)
	}
	return nil
}

// writeSuspensions is the old output.
func writeSuspensions(w io.Writer, st brake.State) error {
	if len(st.Suspensions) == 0 {
		_, err := fmt.Fprintln(w, "No session is suspended.")
		return err
	}
	for _, x := range st.Suspensions {
		_, _ = fmt.Fprintf(w, "SUSPENDED %s: %s over %s (%s) since %s, after %d policy denials in %s\n  reset: cerberus breaker reset %s\n",
			x.ID, x.Principal.Kind, x.Principal.Via, x.Key, x.TrippedAt.Local().Format(time.RFC3339), x.Denials, x.Window, x.ID)
	}
	return nil
}

func brakeReason(r string) string {
	if r == "" {
		return ""
	}
	return ": " + r
}

// statusBanners are the lines `cerberus status` leads with, as the old status did: the
// lockdown, each freeze and the suspended sessions. None when the server cannot say (observe
// mode holds no brakes).
func statusBanners(ctx context.Context, c *apiClient) []string {
	var view gate.BrakesView
	if err := c.get(ctx, "/brakes", &view); err != nil {
		return nil
	}
	return brakeBanners(view.State)
}

func brakeBanners(st brake.State) []string {
	var out []string
	if l := st.Lockdown; l != nil {
		out = append(out, fmt.Sprintf("!!! LOCKDOWN since %s, by %s over %s%s: only plain reads run. Lift: cerberus lockdown --off",
			l.EngagedAt.Local().Format("Jan 2 15:04"), l.By.Kind, l.By.Via, brakeReason(l.Reason)))
	}
	for _, f := range st.Freezes {
		out = append(out, fmt.Sprintf("!!! FREEZE %s on %s since %s%s. Lift: cerberus freeze --off %s", f.ID, f.Match.String(), f.EngagedAt.Local().Format("Jan 2 15:04"), brakeReason(f.Reason), f.ID))
	}
	if n := len(st.Suspensions); n > 0 {
		out = append(out, fmt.Sprintf("!!! BREAKER: %d agent session(s) suspended by the circuit breaker. List: cerberus breaker list; reset: cerberus breaker reset <id>", n))
	}
	return out
}

// parseScope reads --scope key=value pairs into a match, or nil for none (the old parseScope).
func parseScope(pairs []string) (*policy.TargetMatch, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	var m policy.TargetMatch
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || value == "" {
			return nil, fmt.Errorf("--scope takes key=value, not %q", pair)
		}
		set := func(field *string) error {
			if *field != "" {
				return fmt.Errorf("--scope names %s twice", key)
			}
			*field = value
			return nil
		}
		var err error
		switch key {
		case "id":
			err = set(&m.ID)
		case "connector":
			err = set(&m.Connector)
		case "kind":
			err = set(&m.Kind)
		case "env":
			err = set(&m.Env)
		case "owner":
			err = set(&m.Owner)
		case "admin":
			err = set(&m.Admin)
		case "tag":
			m.Tags = append(m.Tags, value)
		default:
			err = fmt.Errorf("--scope key %q is not id, connector, kind, env, owner, admin or tag", key)
		}
		if err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// brakeCommands are the brake verbs against the serving Cerberus.
func (c *cerberus) brakeCommands() []*cobra.Command {
	return brakeCommands(func() (*apiClient, error) { return newAPIClient(c.app, c.home) }, stdIsTerminal)
}
