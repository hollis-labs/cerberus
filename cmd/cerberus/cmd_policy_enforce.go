package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/policy"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

var policyEnforceFlags struct {
	scope string
	id    string
	all   bool
	off   bool
	since time.Duration
}

var policyEnforceCmd = &cobra.Command{
	Use:   "enforce --scope <key=value,...> | --all",
	Short: "Switch enforcement on for a scope, or back to shadow (interactive)",
	Long: `Enforce policy for a scope: the calls it covers are refused, or wait for their
approval, where until now they were only recorded in shadow. Everything else stays
shadow. It is the switch-on (P3-7): start narrow, from what ` + "`cerberus policy report`" + `
shows, and widen as the shadow data allows.

--scope takes the report's terms, all of which must match:

  principal=human|agent|automation   env=prod|staging|dev|...|unknown
  effect=read_sensitive|write|...    connector=<id>   target=<resource>

for example: --scope principal=agent,env=prod

--all enforces everything (mode: enforce). --off takes a scope, or with --all
the whole mode, back to shadow. Going back to shadow is an admin change too.

It runs only on an interactive terminal. It shows the report for the scope over
--since, the would-blocks that would now be refused with no channel to approve
them, and the enforcement before and after, and applies on a typed confirmation
through the same snapshot path as ` + "`cerberus policy apply`" + `. The change is recorded
in the audit log (enforcement_changed), notifies, and shows in ` + "`cerberus status`" + `.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error { return runPolicyEnforce(cmd) },
}

var errPolicyEnforceNotInteractive = errors.New("policy enforce runs only from an interactive terminal, where it shows what enforcing changes and asks you to type a confirmation; run it from a terminal, not a script or an agent")

// enforceEntryFromScope is a report scope as an enforced scope.
func enforceEntryFromScope(scope reportScope, id string) (policy.EnforceEntry, error) {
	entry := policy.EnforceEntry{ID: id}
	for key, value := range scope {
		switch key {
		case "principal":
			entry.Principal = value
		case "env":
			entry.Match.Env = value
		case "connector":
			entry.Match.Connector = value
		case "target":
			entry.Match.ID = value
		case "effect":
			entry.Effect = []contract.Effect{contract.Effect(value)}
		default:
			return entry, fmt.Errorf("--scope %s=%s: an enforced scope is by principal, env, effect, connector or target, not %s", key, value, key)
		}
	}
	return entry, nil
}

func runPolicyEnforce(cmd *cobra.Command) error {
	f := policyEnforceFlags
	if !policyIsTerminal() {
		return errPolicyEnforceNotInteractive
	}
	if (f.scope == "") == !f.all {
		return errors.New("name what to enforce: --scope key=value,... for a scope, or --all for everything")
	}
	scope, err := parseReportScope(f.scope)
	if err != nil {
		return fmt.Errorf("--scope: %w", err)
	}
	entry, err := enforceEntryFromScope(scope, f.id)
	if err != nil {
		return err
	}
	store, err := policyStore()
	if err != nil {
		return err
	}
	if elsewhere, eerr := store.EnforcementDeclaredElsewhere(); eerr != nil {
		return eerr
	} else if len(elsewhere) > 0 {
		return fmt.Errorf("enforcement is also declared in %s; `cerberus policy enforce` owns it in %s, so move it there or remove it, then retry", strings.Join(elsewhere, ", "), policy.EnforcementFileName)
	}
	current, status := store.Load()
	if status.Mismatch() {
		return errors.New("the applied policy snapshot fails its hash check; review the working files and run `cerberus policy apply` before changing enforcement")
	}
	if unapplied, uerr := workingDiffersFromApplied(store, current); uerr != nil {
		return uerr
	} else if unapplied {
		return errors.New("the working policy files have changes that are not applied; review and apply them with `cerberus policy apply` first, so an enforcement change does not carry them in unseen")
	}
	file, err := store.EnforcementFile()
	if err != nil {
		return fmt.Errorf("read %s: %w", policy.EnforcementFileName, err)
	}
	before := current.File().EnforcementOf()
	next := file.EnforcementOf()
	var what, phrase string
	switch {
	case f.all && f.off:
		next.Mode, what, phrase = policy.EnforceShadow, "return everything to shadow except the enforced scopes", "shadow everything"
	case f.all:
		next.Mode, what, phrase = policy.EnforceAll, "enforce everything", "enforce everything"
	case f.off:
		if !next.RemoveEnforceEntry(entry) {
			return fmt.Errorf("no enforced scope is %s; `cerberus posture show` lists them", scope)
		}
		what, phrase = "return "+scope.String()+" to shadow", "shadow "+scope.String()
	default:
		next.SetEnforceEntry(entry)
		what, phrase = "enforce "+scope.String(), "enforce "+scope.String()
	}
	file.Enforcement = &next
	working, problems, err := store.LoadWorkingWithEnforcement(file)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("the working policy files have problems, so nothing was changed:\n  %s", strings.Join(problems, "\n  "))
	}
	after := working.EnforcementOf()
	data, err := policy.Encode(working)
	if err != nil {
		return err
	}
	if policy.Hash(data) == status.Snapshot {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "That is already the applied enforcement; nothing to change.")
		return err
	}

	out := cmd.OutOrStdout()
	if !f.off {
		recs, rerr := readAuditRecords()
		if rerr != nil {
			return rerr
		}
		rep := policyReport(recs, reportOptions{since: time.Now().Add(-f.since), scope: scope, pdp: current, source: "the applied snapshot " + status.Snapshot,
			channels: currentChannelReadiness(cmd.Context())})
		writeEnforceReadiness(out, rep, f.since)
	}
	fmt.Fprintf(out, "\nEnforcement: %s\n         to: %s\n", before.Summary(), after.Summary())
	fmt.Fprintf(out, "\nThis will %s. Type %q to apply: ", what, phrase)
	line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if strings.TrimSpace(line) != phrase {
		return errors.New("the confirmation did not match; nothing was changed")
	}
	restore, err := store.WriteEnforcementFile(file)
	if err != nil {
		return err
	}
	ctx := inProcessContext(cmd.Context())
	applied, err := cerbapi.ApplyPolicy(ctx, policySink(), store, working, 0)
	if err != nil {
		restore()
		return err
	}
	if recErr := cerbapi.RecordEnforcementChange(ctx, policySink(), before, after); recErr != nil {
		return fmt.Errorf("applied %s, but the enforcement change could not be recorded: %w", applied, recErr)
	}
	go notifyOperator("Cerberus: enforcement changed", after.Summary())
	_, err = fmt.Fprintf(out, "Applied %s. Enforcement is now: %s\nThe daemon picks it up on its next decision.\n", applied, after.Summary())
	return err
}

// writeEnforceReadiness is what enforcing the scope would do, from the
// shadow data: how much it covers, what it would block now, and what would
// be refused with no channel ready to approve it.
func writeEnforceReadiness(w io.Writer, rep policyReportData, since time.Duration) {
	fmt.Fprintf(w, "Over the last %s, in this scope: %d decision(s), %d would block now, %d no longer.\n", since, rep.Decided, rep.StillBlock, rep.NoLonger)
	if rep.Channels.Note != "" {
		fmt.Fprintf(w, "Channels: %s\n", rep.Channels.Note)
	}
	if rep.NotReady == 0 {
		fmt.Fprintln(w, "Every would-block has a channel ready to approve it.")
		return
	}
	fmt.Fprintf(w, "! %d would-block(s) would be refused with no channel ready to approve them:\n", rep.NotReady)
	for _, g := range rep.Groups {
		if g.Ready || g.Now == string(policy.Allow) {
			continue
		}
		fmt.Fprintf(w, "  ! %s on %s by %s (%dx, %s): %s\n", strings.Join(g.Operations, ", "), g.Target, g.Principal, g.Count, g.Rule, g.Need)
	}
}

func init() {
	policyEnforceCmd.Flags().StringVar(&policyEnforceFlags.scope, "scope", "", "the scope to enforce, as comma-separated key=value terms")
	policyEnforceCmd.Flags().StringVar(&policyEnforceFlags.id, "id", "", "a name for the enforced scope")
	policyEnforceCmd.Flags().BoolVar(&policyEnforceFlags.all, "all", false, "enforce everything (mode: enforce)")
	policyEnforceCmd.Flags().BoolVar(&policyEnforceFlags.off, "off", false, "return the scope, or with --all the mode, to shadow")
	policyEnforceCmd.Flags().DurationVar(&policyEnforceFlags.since, "since", 7*24*time.Hour, "the shadow data to show for the scope")
	policyCmd.AddCommand(policyEnforceCmd)
}
