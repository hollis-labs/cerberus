package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/gate"
)

// `cerberus audit prune` and `cerberus audit reanchor` (slice 7d): old's verbs, use lines, flags,
// prompts and output (cmd_audit.go on main), over the audit lane's routes, so the serving
// Cerberus writes the chain on its own sink rather than the CLI opening a second writer. Old
// asked for a typed phrase on an interactive terminal; it still does, and the phrase is the
// acknowledgment the route needs. The write is the owner's (decision 0.1): from this CLI the
// first run asks for the owner's approval and prints its id; the owner approves it on the
// console; then the same command with --approval <id> runs it, without the phrase again.

var (
	errAuditPruneNotInteractive    = errors.New("audit prune runs only from an interactive terminal, where it asks for a typed confirmation; run it from a terminal, not a script or an agent, or, once the owner has approved it on the console, with --approval <id>")
	errAuditReanchorNotInteractive = errors.New("audit reanchor runs only from an interactive terminal, where it asks for a typed confirmation; run it from a terminal, not a script or an agent, or, once the owner has approved it on the console, with --approval <id>")
)

// auditReanchorPhrase is old's.
const auditReanchorPhrase = "reanchor"

func auditWriteCommands(client func() (*apiClient, error), isTerminal func() bool) []*cobra.Command {
	var before, pruneApproval, reanchorApproval string
	prune := &cobra.Command{
		Use:   "prune --before <date>",
		Short: "Remove whole audit month files older than a date (interactive only)",
		Long: `Remove the audit month files that lie wholly before a date. Only whole month
files are ever removed, never the newest one, and never part of a file.

This is an admin operation. It runs only from an interactive terminal, asks you
to type a confirmation, and is itself recorded in the log before anything is
removed — if that record cannot be written, nothing is removed. 'cerberus audit
verify' accepts a chain whose earlier files a recorded prune removed.

The serving Cerberus removes them, on the audit chain it writes. Pruning is the
owner's: on the console, signed in; or from this CLI, where the first run asks
for the owner's approval and prints its id, and the same command with
--approval <id> prunes once the owner has approved it on the console.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if pruneApproval == "" && !isTerminal() {
				return errAuditPruneNotInteractive
			}
			if before == "" {
				return errors.New("--before is required, as a date such as 2026-06-01")
			}
			at, err := parseAuditTime(before)
			if err != nil {
				return fmt.Errorf("--before: %w", err)
			}
			cl, err := client()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var plan gate.AuditPrunePlan
			if err = cl.post(cmd.Context(), "/audit/prune/plan", gate.AuditPruneArgs{Before: at}, &plan); err != nil {
				return err
			}
			if len(plan.Files) == 0 {
				_, err = fmt.Fprintf(out, "no audit month file lies wholly before %s; nothing to prune\n", at.Format("2006-01-02"))
				return err
			}
			_, _ = fmt.Fprintf(out, "This removes %d audit month file(s) from %s:\n", len(plan.Files), plan.Dir)
			for _, name := range plan.Files {
				_, _ = fmt.Fprintf(out, "  %s\n", name)
			}
			if pruneApproval == "" {
				phrase := "prune before " + at.Format("2006-01-02")
				typed, aerr := askPhrase(cmd, fmt.Sprintf("Type %q to confirm: ", phrase))
				if aerr != nil {
					return aerr
				}
				if typed != phrase {
					return errors.New("confirmation did not match; nothing was removed")
				}
			}
			var res gate.AuditPruneResult
			err = cl.post(cmd.Context(), "/audit/prune", gate.AuditPruneArgs{Before: at, Acknowledged: true, ApprovalID: pruneApproval}, &res)
			if pending, perr := approvalPending(out, err, pruneApproval); pending {
				return perr
			}
			if err != nil {
				return ownerOnlyHint(err, cl, "the audit log is pruned")
			}
			for _, name := range res.Removed {
				_, _ = fmt.Fprintf(out, "removed %s\n", name)
			}
			return nil
		},
	}
	prune.Flags().StringVar(&before, "before", "", "remove month files wholly before this date (required)")
	prune.Flags().StringVar(&pruneApproval, "approval", "", "prune under this approval, once the owner has approved it on the console")

	reanchor := &cobra.Command{
		Use:   "reanchor",
		Short: "Acknowledge an audit chain that does not verify (interactive only)",
		Long: `Acknowledge an audit chain that does not verify, so that what Cerberus reads
from the log is trusted again from here on.

A chain that does not verify is read with suspicion: a brake engaged anywhere
past the break stays engaged, a lift recorded there is not applied, and lifting
is refused until the chain is reanchored. Check what 'cerberus audit verify'
reports first: a break you did not cause is something to investigate, not
acknowledge.

Reanchoring rewrites nothing. It appends a chain_reanchored record naming the
problems it acknowledges, and 'cerberus audit verify' still reports them. It
runs only from an interactive terminal and asks you to type a confirmation.

The serving Cerberus writes the record, on the audit chain it writes.
Reanchoring is the owner's: on the console, signed in; or from this CLI, where
the first run asks for the owner's approval and prints its id, and the same
command with --approval <id> reanchors once the owner has approved it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if reanchorApproval == "" && !isTerminal() {
				return errAuditReanchorNotInteractive
			}
			cl, err := client()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var plan gate.AuditReanchorPlan
			if err = cl.post(cmd.Context(), "/audit/reanchor/plan", struct{}{}, &plan); err != nil {
				return err
			}
			if plan.TailTrusted {
				_, err = fmt.Fprintln(out, audit.ErrNothingToReanchor.Error())
				return err
			}
			_, _ = fmt.Fprintf(out, "The audit chain in %s has %d problem(s):\n", plan.Dir, len(plan.Problems))
			for _, p := range plan.Problems {
				_, _ = fmt.Fprintf(out, "  %s\n", p)
			}
			if reanchorApproval == "" {
				typed, aerr := askPhrase(cmd, fmt.Sprintf("Type %q to acknowledge them and trust what is written from here on: ", auditReanchorPhrase))
				if aerr != nil {
					return aerr
				}
				if typed != auditReanchorPhrase {
					return errors.New("confirmation did not match; nothing was written")
				}
			}
			var res gate.AuditReanchorResult
			err = cl.post(cmd.Context(), "/audit/reanchor", gate.AuditReanchorArgs{Acknowledged: true, ApprovalID: reanchorApproval}, &res)
			if pending, perr := approvalPending(out, err, reanchorApproval); pending {
				return perr
			}
			if err != nil {
				return ownerOnlyHint(err, cl, "the audit chain is reanchored")
			}
			if res.Seq == 0 {
				_, err = fmt.Fprintln(out, audit.ErrNothingToReanchor.Error())
				return err
			}
			_, err = fmt.Fprintf(out, "reanchored at seq %d; brakes are read from here on, and can be lifted again\n", res.Seq)
			return err
		},
	}
	reanchor.Flags().StringVar(&reanchorApproval, "approval", "", "reanchor under this approval, once the owner has approved it on the console")
	return []*cobra.Command{prune, reanchor}
}
