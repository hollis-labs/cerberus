package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/pipeline"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Tests swap these.
var (
	policyStore = func() (policy.Store, error) {
		dir, err := app.PolicyDir()
		// The audit log is where a snapshot that fails its hash check is
		// enforced from (P3-7).
		auditDir, _ := app.AuditDir()
		return policy.Store{Dir: dir, AuditDir: auditDir}, err
	}
	policyIsTerminal = func() bool {
		return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
	}
	policyDefinitions = defaultPolicyDefinitions
	policyResources   = defaultPolicyResources
	policySink        = app.AuditSink
)

var policyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Explain, apply and report on the operator's policy",
	Long: `Cerberus authorizes every operation against a policy: a built-in baseline by
effect class and principal kind, target defaults from each resource's env and
admin labels, and the operator's files in ~/.cerberus/policy/*.yaml. The most
restrictive matching rule wins.

In this release the policy runs in shadow mode. Every decision is recorded in
the audit log with the rules that matched and whether enforcement would block
it, and nothing is refused on its account. 'cerberus policy report' lists what
would be blocked.

Only the applied snapshot decides. Edit the working files, then run
'cerberus policy apply' in a terminal to make them live.`,
}

var policyExplainFlags struct {
	target, as, output string
	dryRun, adhoc      bool
	working            bool
}

var policyExplainCmd = &cobra.Command{
	Use:   "explain <connector.operation>",
	Short: "Show the decision for an operation and every rule that matched",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		connectorID, operation, ok := strings.Cut(args[0], ".")
		if !ok || connectorID == "" || operation == "" {
			return fmt.Errorf("name the operation as <connector>.<operation>, for example local.deploy or kubernetes.delete_pod")
		}
		defs := policyDefinitions(cmd.Context())
		op, err := findConnectorOperation(defs, connectorID, operation)
		if err != nil {
			return err
		}
		kind := policyExplainFlags.as
		if kind == "" {
			kind = string(cerbapi.DetectCLI().Principal.Kind)
		}
		if kind != "human" && kind != "agent" && kind != "automation" {
			return fmt.Errorf("--as takes human, agent or automation")
		}
		var labels *target.ResourceLabels
		if policyExplainFlags.target != "" {
			for _, r := range policyResources() {
				if r.Labels.ID == policyExplainFlags.target {
					l := r.Labels
					labels = &l
				}
			}
			if labels == nil {
				return fmt.Errorf("resource %q is not in the config; run `cerberus resource list`, or pass --adhoc for a target named by connection settings", policyExplainFlags.target)
			}
		}
		t := target.Resolve(connectorID, op.Target.Kind, policyExplainFlags.target, labels, policyExplainFlags.adhoc)
		req := policy.Request{Connector: connectorID, Operation: operation, Effect: op.Effect, EffectUndeclared: op.EffectUndeclared, Target: t,
			DryRun: policyExplainFlags.dryRun && op.Preview != contract.PreviewNone, Principal: policy.Principal{Kind: kind}}
		pdp, source, err := explainPDP(policyExplainFlags.working)
		if err != nil {
			return err
		}
		res := pdp.Authorize(req)
		posture, postureRules := res.Posture, []int(nil)
		var grantWarnings, egressWarnings []string
		if file, ok := policy.FileOf(pdp); ok {
			_, postureRules = file.PostureFor(req)
			grantWarnings = file.GrantWarnings()
			egressWarnings = file.EgressWarnings()
		}
		if policyExplainFlags.output == outputFormatJSON {
			return printJSON(map[string]any{"request": req, "result": res, "policy": source, "posture": posture, "posture_rules": postureRules, "grant_warnings": grantWarnings, "egress_warnings": egressWarnings, "break_glass": policy.BreakGlassLimitsOf(pdp)})
		}
		if err = writeExplain(cmd.OutOrStdout(), req, res, source); err != nil {
			return err
		}
		writeGrantWarnings(cmd.OutOrStdout(), grantWarnings)
		writeEgressWarnings(cmd.OutOrStdout(), egressWarnings)
		if e, ok := pdp.(interface {
			Enforced(policy.Request) (bool, string)
			Enforcement() (policy.Enforcement, string)
		}); ok {
			on, by := e.Enforced(req)
			_, note := e.Enforcement()
			switch {
			case connectorID == "policy":
				fmt.Fprintln(cmd.OutOrStdout(), "\nEnforcement: never for policy changes, which have their own terminal gate.")
			case on:
				fmt.Fprintf(cmd.OutOrStdout(), "\nEnforcement: ENFORCED, by %s: this decision is applied, not only recorded.\n", by)
			default:
				fmt.Fprintln(cmd.OutOrStdout(), "\nEnforcement: shadow for this call: the decision is recorded and nothing is refused.")
			}
			if note != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "! %s\n", note)
			}
		}
		writeExplainBrakes(cmd.OutOrStdout(), connectorID, req)
		for _, d := range defs {
			if d.ID == connectorID {
				writeExplainCredentials(cmd.OutOrStdout(), d, operation, req.DryRun, t)
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nBreak glass: gets past an approve, never a deny; at most %s.\n", policy.BreakGlassLimitsOf(pdp))
		if cb := policy.CircuitBreakerOf(pdp); cb != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Circuit breaker: an agent session is suspended after %s, until a person resets it (`cerberus breaker list`).\n", cb)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "Circuit breaker: off (policy sets no circuit_breaker).")
		}
		why := "the global posture"
		if len(postureRules) > 0 {
			why = fmt.Sprintf("posture_rules%v", postureRules)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "\nPosture:  %s, from %s (evaluated in shadow, like every decision above)\n", posture, why)
		return err
	},
}

// explainPDP is the applied snapshot, or with working the working files.
func explainPDP(working bool) (policy.PDP, string, error) {
	store, err := policyStore()
	if err != nil {
		return nil, "", err
	}
	if !working {
		pdp, status := store.LoadVerified()
		source := "the applied snapshot " + status.Snapshot
		switch {
		case status.Snapshot == policy.SnapshotBaseline:
			source = "the built-in baseline (no snapshot is applied)"
		case status.Mismatch():
			source = "the built-in baseline: the applied snapshot does not match its hash (" + status.Problem + ")"
		}
		return pdp, source, nil
	}
	f, problems, err := store.LoadWorking()
	if err != nil {
		return nil, "", err
	}
	if len(problems) > 0 {
		return nil, "", fmt.Errorf("the working policy files have problems:\n  %s", strings.Join(problems, "\n  "))
	}
	return policy.NewEvaluator(f, "working"), "the working files (not applied)", nil
}

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
	fmt.Fprintf(w, "%s.%s [%s] on %s: env %s, owner %s, admin %s%s — as %s\n", req.Connector, req.Operation, orUnknown(string(req.Effect)), on, t.Env, t.Owner, t.AdminFor, adhoc, req.Principal.Kind)
	block := "runs once enforced"
	if res.WouldBlock {
		block = "would be blocked once enforced"
	}
	fmt.Fprintf(w, "Decision: %s (%s).\nPolicy:   %s\n\nMatched rules:\n", res.Decision, block, source)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	var rated []string
	for _, m := range res.Matched {
		reason := m.Reason
		if m.Rate != nil {
			reason = strings.TrimSpace(reason + " [rate " + m.Rate.String() + "]")
			rated = append(rated, m.Rule+" "+m.Rate.String())
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", m.Decision, m.Rule, reason)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(rated) > 0 {
		// explain names a principal kind, not a caller, so it has no count
		// to show: the gate counts per principal and effect.
		_, err := fmt.Fprintf(w, "\nRate limits: %s, counted per caller and effect over a sliding window; past it the call is denied (in shadow, a would-block).\n", strings.Join(rated, ", "))
		return err
	}
	return nil
}

var policyApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Make the working policy files the applied snapshot (interactive)",
	Long: `Apply the working policy files in ~/.cerberus/policy. This shows every sampled
decision that changes — each declared operation, against every registered
resource of its connector plus an unregistered and an ad hoc target, for a
human, an agent and automation — and applies only when you type the
confirmation. It runs only from an interactive terminal; policy is never
changed through the socket, the web console or MCP.

The snapshot is written to applied.yaml with its hash in applied.sha256, and
the apply is recorded in the audit log. A snapshot that later fails its hash
check is not used: the baseline decides until you apply again.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !policyIsTerminal() {
			return errPolicyApplyNotInteractive
		}
		store, err := policyStore()
		if err != nil {
			return err
		}
		working, problems, err := store.LoadWorking()
		if err != nil {
			return err
		}
		if len(problems) > 0 {
			return fmt.Errorf("the working policy files have problems, so nothing was applied:\n  %s", strings.Join(problems, "\n  "))
		}
		current, status := store.Load()
		data, err := policy.Encode(working)
		if err != nil {
			return err
		}
		hash := policy.Hash(data)
		if status.Snapshot == hash {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "The working files are already the applied snapshot; nothing to apply.")
			return err
		}
		flips := policy.Flips(current, policy.NewEvaluator(working, hash), policy.Sample(policyDefinitions(cmd.Context()), policyResources()))
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Applying %s over %s.\n", shortHash(hash), status.Snapshot)
		writeFlips(out, flips)
		writeGrantWarnings(out, working.GrantWarnings())
		writeEgressWarnings(out, working.EgressWarnings())
		phrase := "apply " + shortHash(hash)
		fmt.Fprintf(out, "\nType %q to apply: ", phrase)
		line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if strings.TrimSpace(line) != phrase {
			return errors.New("the confirmation did not match; nothing was applied")
		}
		applied, err := cerbapi.ApplyPolicy(inProcessContext(cmd.Context()), policySink(), store, working, len(flips))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Applied %s. The daemon picks it up on its next decision.\n", applied)
		return err
	},
}

var errPolicyApplyNotInteractive = errors.New("policy apply runs only from an interactive terminal, where it shows the decisions that change and asks you to type a confirmation; run it from a terminal, not a script or an agent")

// writeGrantWarnings is the loud part of D5: a session or window grant on
// a prod, shared or not-ours target is the operator's choice, and says so.
func writeGrantWarnings(w io.Writer, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintf(w, "\n! %d rule(s) allow a grant on a protected target:\n", len(warnings))
	for _, warning := range warnings {
		fmt.Fprintf(w, "  ! %s\n", warning)
	}
}

// writeEgressWarnings names the refuse rules that can reach an operation
// that is not a read, and what refuse does there.
func writeEgressWarnings(w io.Writer, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintf(w, "\n! %d egress rule(s) refuse output on operations that are not reads:\n", len(warnings))
	for _, warning := range warnings {
		fmt.Fprintf(w, "  ! %s\n", warning)
	}
}

func writeFlips(w io.Writer, flips []policy.Flip) {
	if len(flips) == 0 {
		fmt.Fprintln(w, "No sampled decision changes.")
		return
	}
	fmt.Fprintf(w, "%d sampled decision(s) change:\n", len(flips))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i, f := range flips {
		if i == 200 {
			fmt.Fprintf(tw, "  … and %d more\n", len(flips)-i)
			break
		}
		c := f.Case
		fmt.Fprintf(tw, "  %s.%s\t%s\t%s\t%s -> %s\t%s\n", c.Connector, c.Operation, targetLabel(c.Target), c.Kind, decisionText(f.From), decisionText(f.To), topRule(f.To))
	}
	_ = tw.Flush()
}

// decidingRule is the first matched rule with the decision's own value.
func decidingRule(p *audit.PolicyDecision) string {
	for _, m := range p.MatchedRules {
		if m.Decision == p.Decision {
			return m.Rule
		}
	}
	return ""
}

func recordTarget(t audit.Target) string {
	name := t.Resource
	if name == "" {
		name = t.Kind
	}
	if t.Adhoc {
		name += " (ad hoc)"
	}
	if t.Resource != "" && (t.Env == "unknown" || t.Env == "") {
		name += " [unlabeled]"
	}
	return name
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

func shortHash(h string) string {
	h = strings.TrimPrefix(h, "sha256:")
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown effect"
	}
	return s
}

// defaultPolicyDefinitions are every operation Cerberus can be asked to run:
// the built-in connectors, the local runtime, pipelines and deploy profiles,
// and, when the daemon is up, its plugins.
func defaultPolicyDefinitions(ctx context.Context) []contract.Definition {
	defs := append(app.NewExternalConnectorService(cfgPath).Definitions(), localconn.Definition(), pipeline.Definition(), infra.Definition())
	seen := map[string]bool{}
	for _, d := range defs {
		seen[d.ID] = true
	}
	if client, err := newResourceSocketClient(); err == nil {
		if remote, listErr := client.ListConnectors(ctx); listErr == nil {
			for _, d := range remote {
				if !seen[d.ID] {
					defs = append(defs, d)
					seen[d.ID] = true
				}
			}
		}
	}
	return defs
}

// defaultPolicyResources are the registered resources, with their labels.
func defaultPolicyResources() []policy.SampleTarget {
	cfg, err := registry.ResolveConfig(cfgPath)
	if err != nil || cfg == nil {
		return nil
	}
	out := make([]policy.SampleTarget, 0, len(cfg.Resources))
	for _, r := range cfg.Resources {
		out = append(out, policy.SampleTarget{Connector: r.Connector, Labels: r.TargetLabels()})
	}
	return out
}

func init() {
	policyCmd.GroupID = "runtime"
	f := policyExplainCmd.Flags()
	f.StringVar(&policyExplainFlags.target, "target", "", "a registered resource id to evaluate against")
	f.StringVar(&policyExplainFlags.as, "as", "", "principal kind: human, agent or automation (default: how this terminal is classified)")
	f.BoolVar(&policyExplainFlags.dryRun, "dry-run", false, "evaluate as a dry run (the plan step)")
	f.BoolVar(&policyExplainFlags.adhoc, "adhoc", false, "evaluate a target named by connection settings rather than a registered resource")
	f.BoolVar(&policyExplainFlags.working, "working", false, "evaluate the working files instead of the applied snapshot")
	addOutputFlag(policyExplainCmd, &policyExplainFlags.output)
	policyCmd.AddCommand(policyExplainCmd, policyApplyCmd, policyReportCmd)
	rootCmd.AddCommand(policyCmd)
}

// writeExplainBrakes names the brake that would refuse a call (§12). The
// brakes run before policy in every mode, so this line outranks the
// decision above.
func writeExplainBrakes(w io.Writer, connectorID string, req policy.Request) {
	if cerbapi.Unbraked(connectorID) {
		return
	}
	st := statusOfBrakes()
	blocked, l, f := st.Blocks(connectorID, req.Effect, req.Target)
	switch {
	case !blocked || req.DryRun:
		if st.Engaged() && !req.DryRun {
			fmt.Fprintln(w, "\nBrakes: engaged, and this call passes them.")
		}
	case l != nil:
		fmt.Fprintf(w, "\nBrakes: REFUSED by lockdown %s%s, whatever the policy says. Lift: cerberus lockdown --off\n", l.ID, brakeReason(l.Reason))
	default:
		fmt.Fprintf(w, "\nBrakes: REFUSED by freeze %s on %s%s, whatever the policy says. Lift: cerberus freeze --off %s\n", f.ID, f.Match.String(), brakeReason(f.Reason), f.ID)
	}
}

// explainBindings are connector-secrets.yaml's credential bindings (I9).
// Tests swap it.
var explainBindings = func() (secrets.BindingFile, error) {
	return secrets.NewReferenceProvider(nil, filepath.Join(filepath.Dir(config.DefaultPath()), "connector-secrets.yaml")).Bindings()
}

// writeExplainCredentials says which binding each of the connector's
// declared credentials would use for this call, or that none would, which
// refuses it.
func writeExplainCredentials(w io.Writer, def contract.Definition, operation string, preview bool, t target.Target) {
	if len(def.Config.Secrets) == 0 {
		return
	}
	file, err := explainBindings()
	if err != nil {
		fmt.Fprintf(w, "\nCredentials: connector-secrets.yaml does not load (%v), so every call needing one fails.\n", err)
		return
	}
	cerbapi.SetCredentialBindings(func() (secrets.BindingFile, error) { return file, nil })
	views := cerbapi.ExplainCredentials(def, operation, preview, t)
	var parts []string
	refused := false
	for _, v := range views {
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
	fmt.Fprintf(w, "\nCredentials: %s.\n", strings.Join(parts, ", "))
	if refused {
		fmt.Fprintln(w, "  This call would be refused as credential_missing, whatever policy decides.")
	}
}
