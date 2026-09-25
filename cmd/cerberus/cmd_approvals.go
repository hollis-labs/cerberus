package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

var approvalsFlags struct{ status, output string }

var approvalsCmd = &cobra.Command{
	Use:   "approvals",
	Short: "List and show approval requests",
	Long: `Operations that policy says need approval wait here as approval requests. An
approval moves from pending to approved or denied, and an approved one is used
once (consumed), expires, or is revoked. Every transition is in the audit log.

The daemon holds the requests. With the daemon down, these commands read its
store directly and say so. Deciding an approval comes in a later release, on a
terminal or the console's approvals page, and never from MCP.`,
}

var approvalsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List approval requests, newest first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		list, source, err := loadApprovals(cmd.Context())
		if err != nil {
			return err
		}
		if approvalsFlags.status != "" {
			var kept []approval.Approval
			for _, a := range list.Approvals {
				if strings.EqualFold(string(a.Status), approvalsFlags.status) {
					kept = append(kept, a)
				}
			}
			list.Approvals = kept
		}
		if approvalsFlags.output == outputFormatJSON {
			return printJSON(list)
		}
		return writeApprovalList(cmd.OutOrStdout(), list, source)
	},
}

var approvalsShowCmd = &cobra.Command{
	Use:   "show <approval-id>",
	Short: "Show one approval request and what happened to it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		list, source, err := loadApprovals(cmd.Context())
		if err != nil {
			return err
		}
		for _, a := range list.Approvals {
			if a.ID == args[0] {
				if approvalsFlags.output == outputFormatJSON {
					return printJSON(a)
				}
				return writeApproval(cmd.OutOrStdout(), a, source)
			}
		}
		return fmt.Errorf("no approval %q; `cerberus approvals list` shows them", args[0])
	},
}

// loadApprovals asks the daemon, and with the daemon down reads its store.
// Either way it is a read: nothing here changes an approval.
func loadApprovals(ctx context.Context) (cerbapi.ApprovalList, string, error) {
	if client, err := newResourceSocketClient(); err == nil {
		list, askErr := client.ListApprovals(ctx)
		var unreachable *cerbapi.DaemonUnreachableError
		switch {
		case askErr == nil:
			return list, "the daemon", nil
		case !errors.As(askErr, &unreachable):
			return cerbapi.ApprovalList{}, "", askErr
		}
	}
	dir, err := app.ApprovalsDir()
	if err != nil {
		return cerbapi.ApprovalList{}, "", err
	}
	store, err := approval.Open(dir)
	if err != nil {
		return cerbapi.ApprovalList{}, "", err
	}
	return cerbapi.ApprovalList{Approvals: store.List(), Problems: store.Problems}, "the store, read directly: the daemon is not running", nil
}

func writeApprovalList(w io.Writer, list cerbapi.ApprovalList, source string) error {
	for _, p := range list.Problems {
		fmt.Fprintf(w, "Warning: the approvals store: %s\n", p)
	}
	if len(list.Approvals) == 0 {
		_, err := fmt.Fprintf(w, "No approval requests (from %s).\n", source)
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tOPERATION\tTARGET\tREQUESTED BY\tCHANNEL\tEXPIRES")
	for _, a := range list.Approvals {
		fmt.Fprintf(tw, "%s\t%s\t%s.%s\t%s\t%s\t%s\t%s\n", a.ID, a.Status, a.Connector, a.Operation, approvalTarget(a),
			who(a.Principal.Kind, a.Principal.Via, a.Principal.Client), a.Channel, expiry(a))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "(from %s)\n", source)
	return err
}

func writeApproval(w io.Writer, a approval.Approval, source string) error {
	fmt.Fprintf(w, "Approval %s: %s\n", a.ID, a.Status)
	fmt.Fprintf(w, "  Operation:    %s.%s [%s]\n", a.Connector, a.Operation, a.Effect)
	fmt.Fprintf(w, "  Target:       %s\n", approvalTarget(a))
	fmt.Fprintf(w, "  Requested by: %s at %s\n", who(a.Principal.Kind, a.Principal.Via, a.Principal.Client), a.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "  Why:          rule %s", a.Rule)
	if a.Reason != "" {
		fmt.Fprintf(w, ": %s", a.Reason)
	}
	fmt.Fprintf(w, "\n  How:          %s, scope %s, expires %s\n", a.Channel, a.Scope, expiry(a))
	if a.PlanHash != "" {
		fmt.Fprintf(w, "  Plan:         %s\n", a.PlanHash)
	}
	if d := a.Decision; d != nil {
		verb := "denied"
		if d.Approve {
			verb = "approved"
		}
		fmt.Fprintf(w, "  Decision:     %s by %s at %s", verb, who(d.By.Kind, d.By.Via, d.By.Client), d.At.UTC().Format(time.RFC3339))
		if d.KeyFingerprint != "" {
			fmt.Fprintf(w, " with key %s", d.KeyFingerprint)
		}
		fmt.Fprintln(w)
	}
	if !a.ConsumedAt.IsZero() {
		fmt.Fprintf(w, "  Used:         %s by operation %s\n", a.ConsumedAt.UTC().Format(time.RFC3339), a.ConsumedOperationID)
	}
	if !a.RevokedAt.IsZero() {
		fmt.Fprintf(w, "  Revoked:      %s\n", a.RevokedAt.UTC().Format(time.RFC3339))
	}
	if a.Status == approval.Pending {
		fmt.Fprintf(w, "  Decide with:  %s\n", a.ApproveWith())
	}
	_, err := fmt.Fprintf(w, "(from %s)\n", source)
	return err
}

func approvalTarget(a approval.Approval) string {
	name := a.Target.Resource
	if name == "" {
		name = a.Target.Kind
	}
	return fmt.Sprintf("%s [%s/%s/%s]", name, orDash(a.Target.Env), orDash(a.Target.Owner), orDash(a.Target.Admin))
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

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func init() {
	approvalsCmd.GroupID = "runtime"
	approvalsCmd.PersistentFlags().StringVarP(&approvalsFlags.output, "output", "o", outputFormatText, "output format: text|json")
	approvalsListCmd.Flags().StringVar(&approvalsFlags.status, "status", "", "only approvals in this status (pending, approved, denied, expired, consumed, revoked)")
	approvalsCmd.AddCommand(approvalsListCmd, approvalsShowCmd)
	rootCmd.AddCommand(approvalsCmd)
}
