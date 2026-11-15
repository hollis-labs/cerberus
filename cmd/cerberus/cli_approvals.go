package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/gate"
)

// The approvals verbs on the CLI: the old `approvals list|show` (cmd/cerberus cmd_approvals.go on
// main), with the old flags and output, over GET /api/approvals[/{id}] with the CLI's key.
//
// Under decision 0.1 an approval is decided only by the owner signed in to the console, so
// `approve` and `deny` decide nothing here: they read the approval and answer, exit 1, where the
// owner decides it (the server's approve_with). With --listen, old pointed its links at the old
// console; the console is now the serving Cerberus itself.

// upgradeNote is main's cerbapi.UpgradeNote: an approval met with the older passkey proof,
// expired by the old build and carried over by the import.
const upgradeNote = "re-approval required: approved with the older passkey proof, which this Cerberus no longer accepts; ask again"

// approvalsSource is what the list and show name as where they read from (old: "the daemon").
const approvalsSource = "the serving Cerberus"

// approvalsCommand is `approvals` with list, show, approve and deny, against the server client
// returns.
func approvalsCommand(client func() (*apiClient, error)) *cobra.Command {
	var output, status, reason string
	var browser bool
	group := &cobra.Command{
		Use:   "approvals",
		Short: "List and show approval requests",
		Long: `Operations that policy says need approval wait here as approval requests. An
approval moves from pending to approved or denied, and an approved one is used
once (consumed), expires, or is revoked. Every transition is in the audit log.

The serving Cerberus holds the requests; these commands read them with the
CLI's key.

A request is decided only by the owner, signed in to the console, on its
approvals page (decision 0.1); never from this CLI or MCP. 'approve' and 'deny'
print where to do it.`,
	}
	group.PersistentFlags().StringVarP(&output, "output", "o", outputText, "output format: text|json")

	list := &cobra.Command{
		Use:   "list",
		Short: "List approval requests, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			var l gate.ApprovalList
			if err = cl.get(cmd.Context(), "/approvals", &l); err != nil {
				return err
			}
			if status != "" {
				var kept []approval.Approval
				for _, a := range l.Approvals {
					if strings.EqualFold(string(a.Status), status) {
						kept = append(kept, a)
					}
				}
				l.Approvals = kept
			}
			if output == outputJSON {
				return printJSON(cmd.OutOrStdout(), l)
			}
			return writeApprovalList(cmd.OutOrStdout(), l, approvalsSource)
		},
	}
	list.Flags().StringVar(&status, "status", "", "only approvals in this status (pending, approved, denied, expired, consumed, revoked)")

	show := &cobra.Command{
		Use:   "show <approval-id>",
		Short: "Show one approval request and what happened to it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := getApproval(cmd, client, args[0])
			if err != nil {
				return err
			}
			if output == outputJSON {
				return printJSON(cmd.OutOrStdout(), a)
			}
			return writeApproval(cmd.OutOrStdout(), a, approvalsSource)
		},
	}

	decide := func(verb string) func(cmd *cobra.Command, args []string) error {
		return func(cmd *cobra.Command, args []string) error {
			a, err := getApproval(cmd, client, args[0])
			if err != nil {
				return err
			}
			if a.Status != approval.Pending {
				return fmt.Errorf("approval %s is %s, not pending; there is nothing to %s", a.ID, a.Status, verb)
			}
			if verb == "approve" {
				return errors.New(gate.ApproveWith(a))
			}
			return fmt.Errorf("%s it on the console: /approvals/%s", verb, a.ID)
		}
	}
	approve := &cobra.Command{
		Use:   "approve <approval-id>",
		Short: "Where the owner approves a pending request: on the console",
		Long: `An approval is decided only by the owner, signed in to the console, on its
approvals page (decision 0.1). This reads the request and prints where to approve
it, exit 1: there is no CLI approve.`,
		Args: cobra.ExactArgs(1),
		RunE: decide("approve"),
	}
	approve.Flags().BoolVar(&browser, "browser", false, "unused: the owner approves on the console (decision 0.1)")
	approve.Flags().StringVar(&reason, "reason", "", "unused: the owner gives the reason on the console (decision 0.1)")
	deny := &cobra.Command{
		Use:   "deny <approval-id>",
		Short: "Where the owner denies a pending request: on the console",
		Long:  "An approval is decided only by the owner, signed in to the console (decision 0.1). This reads the request and prints where to deny it, exit 1.",
		Args:  cobra.ExactArgs(1),
		RunE:  decide("deny"),
	}
	deny.Flags().StringVar(&reason, "reason", "", "unused: the owner gives the reason on the console (decision 0.1)")

	group.AddCommand(list, show, approve, deny)
	return group
}

// getApproval reads one approval, with old's answer for an unknown id.
func getApproval(cmd *cobra.Command, client func() (*apiClient, error), id string) (approval.Approval, error) {
	cl, err := client()
	if err != nil {
		return approval.Approval{}, err
	}
	var a approval.Approval
	err = cl.get(cmd.Context(), "/approvals/"+url.PathEscape(id), &a)
	var refused *apiError
	if errors.As(err, &refused) && refused.Code == "not_found" {
		return a, fmt.Errorf("no approval %q; `cerberus approvals list` shows them", id)
	}
	return a, err
}

// writeApprovalList is old's table.
func writeApprovalList(w io.Writer, list gate.ApprovalList, source string) error {
	for _, p := range list.Problems {
		_, _ = fmt.Fprintf(w, "Warning: the approvals store: %s\n", p)
	}
	if len(list.Approvals) == 0 {
		_, err := fmt.Fprintf(w, "No approval requests (from %s).\n", source)
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tSTATUS\tOPERATION\tTARGET\tREQUESTED BY\tCHANNEL\tEXPIRES")
	for _, a := range list.Approvals {
		status := string(a.Status)
		if a.ExpiredReason == upgradeNote {
			status += " (re-approval required)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s.%s\t%s\t%s\t%s\t%s\n", a.ID, status, a.Connector, a.Operation, approvalTarget(a),
			who(a.Principal.Kind, a.Principal.Via, a.Principal.Client), a.Channel, expiry(a))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "(from %s)\n", source)
	return err
}

// writeApproval is old's record, line for line; where old named a CLI decision, this names the
// console's.
func writeApproval(w io.Writer, a approval.Approval, source string) error {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	p("Approval %s: %s\n", a.ID, a.Status)
	if bg := a.BreakGlass; bg != nil {
		p("  BREAK GLASS:  %q (target typed: %s)\n", bg.Reason, bg.Typed)
		if bg.AckedAt.IsZero() {
			p("  Follow-up:    open; the owner acknowledges it on the console: /approvals/%s\n", a.ID)
		} else {
			p("  Follow-up:    acknowledged at %s\n", bg.AckedAt.UTC().Format(time.RFC3339))
		}
	}
	p("  Operation:    %s.%s [%s]\n", a.Connector, a.Operation, a.Effect)
	p("  Target:       %s\n", approvalTarget(a))
	p("  Requested by: %s at %s\n", who(a.Principal.Kind, a.Principal.Via, a.Principal.Client), a.CreatedAt.UTC().Format(time.RFC3339))
	p("  Why:          rule %s", a.Rule)
	if a.Reason != "" {
		p(": %s", a.Reason)
	}
	p("\n  How:          %s, scope %s, expires %s\n", a.Channel, a.Scope, expiry(a))
	if a.ExpiredReason != "" {
		p("  Expired:      %s\n", a.ExpiredReason)
	}
	if a.PlanHash != "" {
		p("  Plan:         %s\n", a.PlanHash)
	}
	writeShown(w, a.Shown)
	if d := a.Decision; d != nil {
		verb := "denied"
		if d.Approve {
			verb = "approved"
		}
		p("  Decision:     %s by %s at %s", verb, who(d.By.Kind, d.By.Via, d.By.Client), d.At.UTC().Format(time.RFC3339))
		if d.KeyFingerprint != "" {
			p(" with key %s", d.KeyFingerprint)
		}
		if d.SameSurface {
			p(" (passkey, same surface)")
		}
		p("\n")
	}
	if !a.ConsumedAt.IsZero() {
		p("  Used:         %s by operation %s\n", a.ConsumedAt.UTC().Format(time.RFC3339), a.ConsumedOperationID)
	}
	if !a.RevokedAt.IsZero() {
		p("  Revoked:      %s\n", a.RevokedAt.UTC().Format(time.RFC3339))
	}
	if a.Status == approval.Pending {
		p("  Decide with:  %s\n", gate.ApproveWith(a))
	}
	_, err := fmt.Fprintf(w, "(from %s)\n", source)
	return err
}

// writeShown is old's: the plan the approval binds to and the call's arguments, as stored.
func writeShown(w io.Writer, shown *approval.Shown) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	if shown == nil {
		p("  What it runs: not recorded with this approval (it was asked for before approvals stored their plan); decide on the operation and target above, or deny and ask again\n")
		return
	}
	untrusted := map[string]bool{}
	for _, ptr := range shown.Untrusted {
		untrusted[ptr] = true
	}
	section := func(title string, data []byte, note string) {
		if len(data) == 0 {
			return
		}
		p("\n  %s", title)
		if note != "" {
			p("  (%s)", note)
		}
		p("\n")
		var pretty bytes.Buffer
		if json.Indent(&pretty, data, "    ", "  ") != nil {
			pretty.Reset()
			pretty.Write(data)
		}
		p("    %s\n", pretty.String())
	}
	const requester = "written by the requester, not Cerberus: check it, do not take its word"
	planNote := ""
	if untrusted["/plan/preview"] {
		planNote = "its preview echoes the arguments, " + requester
	}
	section("Plan it binds to:", shown.Plan, planNote)
	argsNote := ""
	if untrusted["/arguments"] {
		argsNote = requester
	}
	section("Arguments:", shown.Arguments, argsNote)
	if len(shown.Flagged) > 0 {
		p("\n  ! Look closely at %s: shown as it will run, though it looks like it carries a credential, a command substitution or a hidden character (a hidden character is shown escaped, a field named for a credential is hidden). Nothing the requester wrote was rewritten.\n", strings.Join(shown.Flagged, ", "))
	}
	if shown.Truncated {
		p("  (Part of this was too large to store with the approval; the approval still binds the whole plan by its hash.)\n")
	}
	p("\n")
}

func approvalTarget(a approval.Approval) string {
	name := a.Target.Resource
	if name == "" {
		name = a.Target.Kind
	}
	return fmt.Sprintf("%s [%s/%s/%s]", name, valueOrDash(a.Target.Env), valueOrDash(a.Target.Owner), valueOrDash(a.Target.Admin))
}

func who(kind, via, client string) string {
	s := kind
	if via != "" {
		s += " via " + via
	}
	if client != "" {
		s += " (" + client + ")"
	}
	return s
}

func expiry(a approval.Approval) string {
	if a.ExpiresAt.IsZero() {
		return "-"
	}
	return a.ExpiresAt.UTC().Format(time.RFC3339)
}

// approvalsCommand is the approvals group against the serving Cerberus.
func (c *cerberus) approvalsCommand() *cobra.Command {
	return approvalsCommand(func() (*apiClient, error) { return newAPIClient(c.app, c.home) })
}
