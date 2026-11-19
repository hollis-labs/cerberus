package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
)

// `cerberus policy` and `cerberus posture` (slice 7d): old's verbs, use lines, flags and output
// (cmd_policy.go, cmd_policy_enforce.go, policy_report.go and cmd_posture.go on main) over the
// policy routes, so the serving Cerberus, which holds the policy and the audit log, reads and
// applies them. The writes ask what old asked on a terminal: the change, then the typed phrase,
// which is the acknowledgment. They are the owner's (decision 0.1): from this CLI the first run
// asks for the owner's approval and prints its id; the owner approves it on the console; then
// the same command with --approval <id> applies exactly the change approved, without the phrase.

var (
	errPolicyApplyNotInteractive   = errors.New("policy apply runs only from an interactive terminal, where it shows the decisions that change and asks you to type a confirmation; run it from a terminal, not a script or an agent, or, once the owner has approved it on the console, with --approval <id>")
	errPolicyEnforceNotInteractive = errors.New("policy enforce runs only from an interactive terminal, where it shows what enforcing changes and asks you to type a confirmation; run it from a terminal, not a script or an agent, or, once the owner has approved it on the console, with --approval <id>")
	errPostureNotInteractive       = errors.New("the posture is changed only from an interactive terminal, where the change is shown and you type a confirmation; run `cerberus posture set` in your terminal, not from a script or an agent, or, once the owner has approved it on the console, with --approval <id>")
)

const ownerPolicyNote = `

Changing policy is the owner's: on the console, signed in; or from this CLI,
where the first run asks for the owner's approval and prints its id, and the
same command with --approval <id> applies exactly that change once the owner
has approved it on the console.`

func policyCommands(client func() (*apiClient, error), isTerminal func() bool) []*cobra.Command {
	group := &cobra.Command{
		Use:   "policy",
		Short: "Explain, apply and report on the operator's policy",
		Long: `Cerberus authorizes every operation against a policy: a built-in baseline by
effect class and principal kind, target defaults from each resource's env and
admin labels, and the operator's files in the config directory's policy/. The
most restrictive matching rule wins.

Every decision is recorded in the audit log with the rules that matched and
whether enforcement would block it. 'cerberus policy report' lists what would
be blocked; 'cerberus policy enforce' switches enforcement on for a scope.

Only the applied snapshot decides. Edit the working files, then run
'cerberus policy apply' to make them live.`,
	}
	var ex api.PolicyExplainArgs
	var exOutput string
	explain := &cobra.Command{
		Use:   "explain <connector.operation>",
		Short: "Show the decision for an operation and every rule that matched",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			req := ex
			req.Operation = args[0]
			var out api.PolicyExplain
			if err = cl.post(cmd.Context(), "/policy/explain", req, &out); err != nil {
				return err
			}
			text := out.Text
			out.Text = nil
			if exOutput == outputJSON {
				return printJSON(cmd.OutOrStdout(), out)
			}
			return writeExplainAll(cmd.OutOrStdout(), out, text)
		},
	}
	ef := explain.Flags()
	ef.StringVar(&ex.Target, "target", "", "a registered resource id to evaluate against")
	ef.StringVar(&ex.As, "as", "", "principal kind: human, agent or automation (default: how this terminal is classified)")
	ef.BoolVar(&ex.DryRun, "dry-run", false, "evaluate as a dry run (the plan step)")
	ef.BoolVar(&ex.Adhoc, "adhoc", false, "evaluate a target named by connection settings rather than a registered resource")
	ef.BoolVar(&ex.Working, "working", false, "evaluate the working files instead of the applied snapshot")
	ef.StringVarP(&exOutput, "output", "o", outputText, "output format: text|json")

	var applyApproval string
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Make the working policy files the applied snapshot (interactive)",
		Long: `Apply the working policy files. This shows every sampled decision that
changes — each declared operation, against every registered resource of its
connector plus an unregistered and an ad hoc target, for a human, an agent and
automation — and applies only when you type the confirmation.

The snapshot is written to applied.yaml with its hash in applied.sha256, and
the apply is recorded in the audit log. A snapshot that later fails its hash
check is not used: the baseline decides until you apply again.` + ownerPolicyNote,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPolicyChange(cmd, client, isTerminal, errPolicyApplyNotInteractive, "/policy/apply", &api.PolicyChangeArgs{ApprovalID: applyApproval}, nil,
				func(out io.Writer, ch api.PolicyChange) {
					_, _ = fmt.Fprintf(out, "Applying %s over %s.\n", api.ShortHash(ch.Hash), ch.Over)
					writeFlips(out, ch.Flips)
					writeGrantWarnings(out, ch.GrantWarnings)
					writeEgressWarnings(out, ch.EgressWarnings)
					_, _ = fmt.Fprintln(out)
				}, "Type %q to apply: ", "the confirmation did not match; nothing was applied",
				func(out io.Writer, res api.PolicyApplied) error {
					_, err := fmt.Fprintf(out, "Applied %s. The serving Cerberus picks it up on its next decision.\n", res.Applied)
					return err
				})
		},
	}
	apply.Flags().StringVar(&applyApproval, "approval", "", "apply under this approval, once the owner has approved it on the console")

	var enf api.PolicyEnforceArgs
	enforce := &cobra.Command{
		Use:   "enforce --scope <key=value,...> | --all",
		Short: "Switch enforcement on for a scope, or back to shadow (interactive)",
		Long: `Enforce policy for a scope: the calls it covers are refused, or wait for their
approval, where until now they were only recorded in shadow. Everything else stays
shadow. Start narrow, from what ` + "`cerberus policy report`" + ` shows, and widen as the
shadow data allows.

--scope takes the report's terms, all of which must match:

  principal=human|agent|automation   env=prod|staging|dev|...|unknown
  effect=read_sensitive|write|...    connector=<id>   target=<resource>

for example: --scope principal=agent,env=prod

--all enforces everything (mode: enforce). --off takes a scope, or with --all
the whole mode, back to shadow. Going back to shadow is an admin change too.

It shows the report for the scope over --since, the would-blocks that would now
be refused with no channel to approve them, and the enforcement before and
after, and applies on a typed confirmation through the same snapshot path as
` + "`cerberus policy apply`" + `. The change is recorded in the audit log
(enforcement_changed).` + ownerPolicyNote,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (enf.Scope == "") == !enf.All {
				return errors.New("name what to enforce: --scope key=value,... for a scope, or --all for everything")
			}
			if _, err := api.ParseReportScope(enf.Scope); err != nil {
				return fmt.Errorf("--scope: %w", err)
			}
			args := enf
			return runPolicyChange(cmd, client, isTerminal, errPolicyEnforceNotInteractive, "/policy/enforce", &args.PolicyChangeArgs, &args,
				func(out io.Writer, ch api.PolicyChange) {
					if ch.Readiness != nil {
						writeEnforceReadiness(out, *ch.Readiness, ch.Since)
					}
					_, _ = fmt.Fprintf(out, "\nEnforcement: %s\n         to: %s\n", ch.EnforcementBefore, ch.EnforcementAfter)
					_, _ = fmt.Fprintf(out, "\nThis will %s. ", ch.What)
				}, "Type %q to apply: ", "the confirmation did not match; nothing was changed",
				func(out io.Writer, res api.PolicyApplied) error {
					_, err := fmt.Fprintf(out, "Applied %s. Enforcement is now: %s\nThe serving Cerberus picks it up on its next decision.\n", res.Applied, res.Enforcement)
					return err
				})
		},
	}
	enforce.Flags().StringVar(&enf.Scope, "scope", "", "the scope to enforce, as comma-separated key=value terms")
	enforce.Flags().StringVar(&enf.ID, "id", "", "a name for the enforced scope")
	enforce.Flags().BoolVar(&enf.All, "all", false, "enforce everything (mode: enforce)")
	enforce.Flags().BoolVar(&enf.Off, "off", false, "return the scope, or with --all the mode, to shadow")
	enforce.Flags().DurationVar(&enf.Since, "since", 7*24*time.Hour, "the shadow data to show for the scope")
	enforce.Flags().StringVar(&enf.ApprovalID, "approval", "", "apply under this approval, once the owner has approved it on the console")

	var rf struct{ since, until, scope, output string }
	report := &cobra.Command{
		Use:   "report",
		Short: "Summarize what policy would block, and what enforcing it would need",
		Long: `Summarize the operations policy would have blocked, from the decisions shadow
mode recorded in the audit log. This is the data for choosing what to enforce
first.

Each would-block is decided again under the policy applied now, so the report
says what enforcement would do today, not only what it would have done: a
decision the current policy no longer blocks is counted apart. Groups are by
the rule that decided, the principal kind and the target, most frequent first,
with the operations in each.

For each group it names the approval that would be needed and its channel
(tty_confirm or out_of_band). The owner approves either on the console, so both
are ready while the serving Cerberus runs; a rule asking for more than one
approver cannot be met, and a deny cannot be approved.

--scope narrows the report to what an enforcement scope would cover, as
comma-separated key=value terms, all of which must match:

  principal=human|agent|automation   env=prod|staging|dev|...|unknown
  effect=read_sensitive|write|...    connector=<id>   target=<resource>
  rule=<rule name>

for example: --scope principal=agent,env=prod`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			since, err := parseAuditTime(rf.since)
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			until, err := parseAuditTime(rf.until)
			if err != nil {
				return fmt.Errorf("--until: %w", err)
			}
			if _, err = api.ParseReportScope(rf.scope); err != nil {
				return fmt.Errorf("--scope: %w", err)
			}
			cl, err := client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if !since.IsZero() {
				q.Set("since", since.Format(time.RFC3339Nano))
			}
			if !until.IsZero() {
				q.Set("until", until.Format(time.RFC3339Nano))
			}
			if rf.scope != "" {
				q.Set("scope", rf.scope)
			}
			path := "/policy/report"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var rep api.PolicyReport
			if err = cl.get(cmd.Context(), path, &rep); err != nil {
				return err
			}
			if rf.output == outputJSON {
				return printJSON(cmd.OutOrStdout(), rep)
			}
			return writePolicyReport(cmd.OutOrStdout(), rep)
		},
	}
	report.Flags().StringVar(&rf.since, "since", "", "decisions at or after this date or time")
	report.Flags().StringVar(&rf.until, "until", "", "decisions before this date or time")
	report.Flags().StringVar(&rf.scope, "scope", "", "only decisions in this scope: key=value terms, comma-separated (principal, env, effect, connector, target, rule)")
	report.Flags().StringVarP(&rf.output, "output", "o", outputText, "output format: text|json")

	group.AddCommand(explain, apply, enforce, report)
	return []*cobra.Command{group, postureCommand(client, isTerminal)}
}

func postureCommand(client func() (*apiClient, error), isTerminal func() bool) *cobra.Command {
	group := &cobra.Command{
		Use:   "posture",
		Short: "Show or change the posture: secure by default, permissive by choice",
		Long: `The posture decides how strict Cerberus is (section 13 of the security plan).

  secure      the default: the policy baseline, the target defaults, plugin
              install review, changed-hash refusal, and opt-in MCP exposure
  permissive  opted into: the built-in strictness steps aside

The global posture governs the host-wide switches, which have no target, and is
where policy evaluation starts. Posture rules, scoped with --scope, change only
the policy evaluation of the targets they match. A permissive rule reaches only
targets whose env and owner are declared and that are not ad hoc.

Nothing relaxes the audit log, credential redaction, the --ack gate, or the
deny on a target labeled admin: owner.

The posture is part of the applied policy snapshot. 'cerberus posture set'
writes policy/posture.yaml and applies it the way 'cerberus policy apply' does:
after showing what changes, on a typed confirmation, recorded in the audit log.`,
	}
	var output string
	show := &cobra.Command{
		Use:   "show",
		Short: "Show the applied posture and what it relaxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			var ps api.PostureShow
			if err = cl.get(cmd.Context(), "/posture", &ps); err != nil {
				return err
			}
			if output == outputJSON {
				return printJSON(cmd.OutOrStdout(), ps)
			}
			return writePostureShow(cmd.OutOrStdout(), ps)
		},
	}
	show.Flags().StringVarP(&output, "output", "o", outputText, "output format: text|json")
	change := func(path string, args *api.PostureChangeArgs) func(*cobra.Command) error {
		return func(cmd *cobra.Command) error {
			return runPolicyChange(cmd, client, isTerminal, errPostureNotInteractive, path, &args.PolicyChangeArgs, args,
				func(out io.Writer, ch api.PolicyChange) {
					_, _ = fmt.Fprintf(out, "Posture: %s\n     to: %s\n\n", *ch.PostureBefore, *ch.PostureAfter)
					writeHostWideChange(out, ch.PostureBefore.Global, ch.PostureAfter.Global)
					_, _ = fmt.Fprintln(out)
					writeFlips(out, ch.Flips)
					_, _ = fmt.Fprintf(out, "\nThis will %s. ", ch.What)
				}, "Type %q to apply: ", "the confirmation did not match; nothing was changed",
				func(out io.Writer, res api.PolicyApplied) error {
					_, err := fmt.Fprintf(out, "Applied %s. The posture is now: %s\n", res.Applied, *res.Posture)
					return err
				})
		}
	}
	var setArgs, resetArgs api.PostureChangeArgs
	set := &cobra.Command{
		Use:   "set <secure|permissive>",
		Short: "Set the global posture, or with --scope a posture rule (interactive)",
		Long: `Set the global posture, or with --scope add a posture rule for the targets that
match. A scope is one or more key=value pairs: id, connector, kind, env, owner,
admin, or tag, which may repeat. A value prefixed with "!" matches anything
else. For example:

  cerberus posture set permissive                     # everything
  cerberus posture set permissive --scope env=dev --scope owner=self
  cerberus posture set secure --scope id=payments-db  # keep one target strict

A rule with the same scope is replaced. It shows the host-wide switches and the
sampled policy decisions that change, and applies on a typed confirmation.` + ownerPolicyNote,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, a []string) error {
			if a[0] != policy.PostureSecure && a[0] != policy.PosturePermissive {
				return fmt.Errorf("the posture is secure or permissive, not %q", a[0])
			}
			setArgs.Posture = a[0]
			return change("/posture/set", &setArgs)(cmd)
		},
	}
	reset := &cobra.Command{
		Use:   "reset",
		Short: "Return to the secure default, or with --scope remove one posture rule (interactive)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return change("/posture/reset", &resetArgs)(cmd) },
	}
	for c, args := range map[*cobra.Command]*api.PostureChangeArgs{set: &setArgs, reset: &resetArgs} {
		c.Flags().StringArrayVar(&args.Scope, "scope", nil, "a posture rule's scope, as key=value (repeatable)")
		c.Flags().StringVar(&args.ApprovalID, "approval", "", "apply under this approval, once the owner has approved it on the console")
	}
	group.AddCommand(show, set, reset)
	return group
}

// runPolicyChange is a write as old ran it: the change shown (the plan form), the typed phrase
// on a terminal, then the write with the hash shown and the acknowledgment. With --approval the
// phrase is not asked again: the owner approved exactly this change. body is the request (args
// embedded in it), or nil for args alone.
func runPolicyChange(cmd *cobra.Command, client func() (*apiClient, error), isTerminal func() bool, notInteractive error, path string,
	args *api.PolicyChangeArgs, body any, show func(io.Writer, api.PolicyChange), prompt, mismatch string, done func(io.Writer, api.PolicyApplied) error) error {
	approvalID := args.ApprovalID
	if approvalID == "" && !isTerminal() {
		return notInteractive
	}
	if body == nil {
		body = args
	}
	cl, err := client()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	args.ApprovalID, args.Acknowledged, args.Hash = "", false, ""
	var ch api.PolicyChange
	if err = cl.post(cmd.Context(), path+"/plan", body, &ch); err != nil {
		return err
	}
	if ch.Nothing != "" {
		_, err = fmt.Fprintln(out, ch.Nothing)
		return err
	}
	show(out, ch)
	if approvalID != "" {
		_, _ = fmt.Fprintf(out, "Approved by the owner as %s.\n", approvalID)
	} else {
		typed, aerr := askPhrase(cmd, fmt.Sprintf(prompt, ch.Phrase))
		if aerr != nil {
			return aerr
		}
		if typed != ch.Phrase {
			return errors.New(mismatch)
		}
	}
	args.ApprovalID, args.Acknowledged, args.Hash = approvalID, true, ch.Hash
	var res api.PolicyApplied
	err = cl.post(cmd.Context(), path, body, &res)
	if pending, perr := approvalPending(out, err, approvalID); pending {
		return perr
	}
	if err != nil {
		return ownerOnlyHint(err, cl, "policy is changed")
	}
	return done(out, res)
}

// writeExplainAll is old's explain text: the decision and its rules, then enforcement, brakes,
// credentials, break glass, the circuit breaker and the posture.
func writeExplainAll(w io.Writer, ex api.PolicyExplain, text *api.PolicyExplainText) error {
	if err := writeExplain(w, ex.Request, ex.Result, ex.Policy); err != nil {
		return err
	}
	writeGrantWarnings(w, ex.GrantWarnings)
	writeEgressWarnings(w, ex.EgressWarnings)
	if text == nil {
		text = &api.PolicyExplainText{}
	}
	switch text.Enforcement {
	case "policy":
		_, _ = fmt.Fprintln(w, "\nEnforcement: never for policy changes, which have their own terminal gate.")
	case "enforced":
		_, _ = fmt.Fprintf(w, "\nEnforcement: ENFORCED, by %s: this decision is applied, not only recorded.\n", text.EnforcedBy)
	case "shadow":
		_, _ = fmt.Fprintln(w, "\nEnforcement: shadow for this call: the decision is recorded and nothing is refused.")
	}
	if text.Enforcement != "" && text.EnforcementNote != "" {
		_, _ = fmt.Fprintf(w, "! %s\n", text.EnforcementNote)
	}
	if text.Brakes != "" {
		_, _ = fmt.Fprintf(w, "\n%s\n", text.Brakes)
	}
	switch {
	case text.CredentialsError != "":
		_, _ = fmt.Fprintf(w, "\nCredentials: connector-secrets.yaml does not load (%s), so every call needing one fails.\n", text.CredentialsError)
	case len(text.Credentials) > 0:
		var parts []string
		refused := false
		for _, v := range text.Credentials {
			switch {
			case v.None:
				refused = true
				parts = append(parts, v.Name+" (none: no credential for this access)")
			case v.Label == "":
				parts = append(parts, v.Name+" (the legacy chain)")
			default:
				parts = append(parts, v.Name)
			}
		}
		_, _ = fmt.Fprintf(w, "\nCredentials: %s.\n", strings.Join(parts, ", "))
		if refused {
			_, _ = fmt.Fprintln(w, "  This call would be refused as credential_missing, whatever policy decides.")
		}
	}
	_, _ = fmt.Fprintf(w, "\nBreak glass: gets past an approve, never a deny; at most %s.\n", ex.BreakGlass)
	if text.CircuitBreaker != "" {
		_, _ = fmt.Fprintf(w, "Circuit breaker: an agent session is suspended after %s, until a person resets it (`cerberus breaker list`).\n", text.CircuitBreaker)
	} else {
		_, _ = fmt.Fprintln(w, "Circuit breaker: off (policy sets no circuit_breaker).")
	}
	why := "the global posture"
	if len(ex.PostureRules) > 0 {
		why = fmt.Sprintf("posture_rules%v", ex.PostureRules)
	}
	_, err := fmt.Fprintf(w, "\nPosture:  %s, from %s (evaluated in shadow, like every decision above)\n", ex.Posture, why)
	return err
}

// writeExplain is old's.
func writeExplain(w io.Writer, req policy.Request, res policy.Result, source string) error {
	t := req.Target
	on := t.Kind
	if t.ID != "" {
		on = t.ID + " (" + t.Kind + ")"
	}
	adhoc := ""
	if t.Adhoc {
		adhoc = ", ad hoc"
	}
	_, _ = fmt.Fprintf(w, "%s.%s [%s] on %s: env %s, owner %s, admin %s%s — as %s\n", req.Connector, req.Operation, orUnknownEffect(string(req.Effect)), on, t.Env, t.Owner, t.AdminFor, adhoc, req.Principal.Kind)
	block := "runs once enforced"
	if res.WouldBlock {
		block = "would be blocked once enforced"
	}
	_, _ = fmt.Fprintf(w, "Decision: %s (%s).\nPolicy:   %s\n\nMatched rules:\n", res.Decision, block, source)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	var rated []string
	for _, m := range res.Matched {
		reason := m.Reason
		if m.Rate != nil {
			reason = strings.TrimSpace(reason + " [rate " + m.Rate.String() + "]")
			rated = append(rated, m.Rule+" "+m.Rate.String())
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", m.Decision, m.Rule, reason)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(rated) > 0 {
		_, err := fmt.Fprintf(w, "\nRate limits: %s, counted per caller and effect over a sliding window; past it the call is denied (in shadow, a would-block).\n", strings.Join(rated, ", "))
		return err
	}
	return nil
}

func orUnknownEffect(s string) string {
	if s == "" {
		return "unknown effect"
	}
	return s
}

func writeGrantWarnings(w io.Writer, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "\n! %d rule(s) allow a grant on a protected target:\n", len(warnings))
	for _, warning := range warnings {
		_, _ = fmt.Fprintf(w, "  ! %s\n", warning)
	}
}

func writeEgressWarnings(w io.Writer, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "\n! %d egress rule(s) refuse output on operations that are not reads:\n", len(warnings))
	for _, warning := range warnings {
		_, _ = fmt.Fprintf(w, "  ! %s\n", warning)
	}
}

func writeFlips(w io.Writer, flips []policy.Flip) {
	if len(flips) == 0 {
		_, _ = fmt.Fprintln(w, "No sampled decision changes.")
		return
	}
	_, _ = fmt.Fprintf(w, "%d sampled decision(s) change:\n", len(flips))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i, f := range flips {
		if i == 200 {
			_, _ = fmt.Fprintf(tw, "  … and %d more\n", len(flips)-i)
			break
		}
		c := f.Case
		_, _ = fmt.Fprintf(tw, "  %s.%s\t%s\t%s\t%s -> %s\t%s\n", c.Connector, c.Operation, targetLabel(c.Target), c.Kind, decisionText(f.From), decisionText(f.To), topRule(f.To))
	}
	_ = tw.Flush()
}

func targetLabel(t target.Target) string {
	name := t.ID
	if name == "" {
		name = t.Kind
	}
	return fmt.Sprintf("%s [%s/%s/%s]", name, t.Env, t.Owner, t.AdminFor)
}

func decisionText(r policy.Result) string {
	if r.WouldBlock {
		return string(r.Decision) + "*"
	}
	return string(r.Decision)
}

func topRule(r policy.Result) string {
	for _, m := range r.Matched {
		if m.Decision == r.Decision {
			return m.Rule
		}
	}
	return ""
}

// writeEnforceReadiness is old's.
func writeEnforceReadiness(w io.Writer, rep api.PolicyReport, since time.Duration) {
	_, _ = fmt.Fprintf(w, "Over the last %s, in this scope: %d decision(s), %d would block now, %d no longer.\n", since, rep.Decided, rep.StillBlock, rep.NoLonger)
	if rep.Channels.Note != "" {
		_, _ = fmt.Fprintf(w, "Channels: %s\n", rep.Channels.Note)
	}
	if rep.NotReady == 0 {
		_, _ = fmt.Fprintln(w, "Every would-block has a channel ready to approve it.")
		return
	}
	_, _ = fmt.Fprintf(w, "! %d would-block(s) would be refused with no channel ready to approve them:\n", rep.NotReady)
	for _, g := range rep.Groups {
		if g.Ready || g.Now == string(policy.Allow) {
			continue
		}
		_, _ = fmt.Fprintf(w, "  ! %s on %s by %s (%dx, %s): %s\n", strings.Join(g.Operations, ", "), g.Target, g.Principal, g.Count, g.Rule, g.Need)
	}
}

// writePolicyReport is old's.
func writePolicyReport(w io.Writer, rep api.PolicyReport) error {
	scope := ""
	if rep.Scope != "" {
		scope = " in scope " + rep.Scope
	}
	_, _ = fmt.Fprintf(w, "%d of %d recorded decision(s)%s would be blocked once enforced.\n", rep.WouldBlock, rep.Decided, scope)
	if rep.Decided == 0 {
		_, _ = fmt.Fprintln(w, "No shadow decisions are recorded yet. The daemon records one for every gated operation a person or an agent asks for "+
			"(automation, such as plugin loads at start, is not authorized); use Cerberus as usual for a while, then run this again.")
	}
	defer writeEgressSummary(w, rep.Egress)
	defer writeChannels(w, rep)
	if rep.WouldBlock == 0 {
		return nil
	}
	_, _ = fmt.Fprintf(w, "Under %s: %d still would, %d no longer would.\n", rep.Policy, rep.StillBlock, rep.NoLonger)
	channels := make([]string, 0, len(rep.ByChannel))
	for ch, n := range rep.ByChannel {
		channels = append(channels, fmt.Sprintf("%s %d", ch, n))
	}
	sort.Strings(channels)
	if len(channels) > 0 {
		_, _ = fmt.Fprintf(w, "Approvals needed: %s.\n", strings.Join(channels, ", "))
	}
	if rep.NotReady > 0 {
		_, _ = fmt.Fprintf(w, "! %d would-block decision(s) need a channel that is not ready: enforcing them now would refuse them with no way to approve.\n", rep.NotReady)
	}
	_, _ = fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer func() { _, _ = fmt.Fprintln(w) }()
	_, _ = fmt.Fprintln(tw, "COUNT\tRULE\tPRINCIPAL\tTARGET\tOPERATIONS\tNOW\tNEEDS\tLAST")
	for _, r := range rep.Groups {
		need := r.Need
		if !r.Ready {
			need = "NOT READY: " + need
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s [%s]\t%s\t%s\t%s\t%s\n", r.Count, orDashPolicy(r.Rule), r.Principal, r.Target, r.Labels,
			strings.Join(r.Operations, " "), r.Now, need, r.Last.UTC().Format(time.RFC3339))
	}
	return tw.Flush()
}

func orDashPolicy(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// writeChannels is old's line, for the console: the owner approves either channel there.
func writeChannels(w io.Writer, rep api.PolicyReport) {
	oob := "out_of_band ready (the owner approves on the console)"
	if !rep.Channels.OutOfBand {
		oob = "out_of_band NOT READY: " + rep.Channels.Note
	}
	held := "tty_confirm ready: the serving Cerberus holds pending approvals for the owner to decide on the console"
	if !rep.Channels.Daemon {
		held = "tty_confirm NOT READY: nothing holds a pending approval"
	}
	_, _ = fmt.Fprintf(w, "Channels: %s; %s.\n", oob, held)
}

func writeEgressSummary(w io.Writer, rows []api.EgressRow) {
	if len(rows) == 0 {
		return
	}
	shadow := 0
	for _, r := range rows {
		if !r.Applied {
			shadow += r.Count
		}
	}
	_, _ = fmt.Fprintf(w, "\nEgress: %d decision group(s); %d decision(s) recorded in shadow (would have applied).\n", len(rows), shadow)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "COUNT\tRULE\tLABEL\tACTION\tMODE\tPRINCIPAL\tTARGET\tWITHHELD\tLAST")
	for _, r := range rows {
		mode := "shadow"
		if r.Applied {
			mode = "applied"
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", r.Count, r.Rule, r.Label, r.Action, mode, r.Principal, r.Target, r.Withheld, r.Last.UTC().Format(time.RFC3339))
	}
	_ = tw.Flush()
}

func writeHostWideChange(w io.Writer, from, to string) {
	if from == to {
		_, _ = fmt.Fprintf(w, "Host-wide switches: unchanged (the global posture stays %s).\n", to)
		return
	}
	_, _ = fmt.Fprintf(w, "Host-wide switches change (the global posture goes %s -> %s):\n", from, to)
	for _, r := range api.HostWide {
		was, now := r.Secure, r.Permissive
		if to == policy.PostureSecure {
			was, now = r.Permissive, r.Secure
		}
		note := ""
		if !r.Live {
			note = " (in a later release; recorded in the snapshot now)"
		}
		_, _ = fmt.Fprintf(w, "  %s: %s -> %s%s\n", r.Switch, was, now, note)
	}
}

func writePostureShow(w io.Writer, ps api.PostureShow) error {
	s := ps.Posture
	_, _ = fmt.Fprintf(w, "Posture: %s\n", s)
	_, _ = fmt.Fprintf(w, "Snapshot: %s\n\nHost-wide switches (the global posture, %s):\n", s.Snapshot, s.Global)
	for _, r := range ps.HostWide {
		note := ""
		if live, _ := r["live"].(bool); !live {
			note = " (in a later release)"
		}
		_, _ = fmt.Fprintf(w, "  %s: %s%s\n", r["switch"], r["now"], note)
	}
	if len(s.Rules) > 0 {
		_, _ = fmt.Fprintln(w, "\nPosture rules (policy evaluation of the targets they match):")
		for _, r := range s.Rules {
			_, _ = fmt.Fprintf(w, "  %s: %s\n", r.Match, r.Posture)
		}
	}
	if s.Enforcement != "" {
		_, _ = fmt.Fprintf(w, "\nEnforcement: %s (change it with `cerberus policy enforce`).\n", s.Enforcement)
	}
	if s.BreakGlass.PerTarget > 0 {
		_, _ = fmt.Fprintf(w, "\nBreak glass: at most %s (break_glass in policy; change it with `cerberus policy apply`).\n", s.BreakGlass)
	}
	_, _ = fmt.Fprintf(w, "\nAlways enforced, whatever the enforcement above: %s. Policy's decision on those is applied, in shadow too.\n", policy.BaselineEnforcedSummary)
	_, _ = fmt.Fprintln(w, "Enforced only where the enforcement above covers it: everything else, including a person's calls and targets with no env or admin label.")
	_, err := fmt.Fprintln(w, "Always on: the audit log, and credential redaction. The --ack gate is an intent check the caller sets itself, not a person's approval.")
	return err
}

func (c *cerberus) policyCommands() []*cobra.Command {
	return policyCommands(func() (*apiClient, error) { return newAPIClient(c.app, c.home) }, stdIsTerminal)
}
