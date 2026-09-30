package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/presence"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

var policyReportFlags struct{ since, until, scope, output string }

var policyReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Summarize what policy would block, and what enforcing it would need",
	Long: `Summarize the operations policy would have blocked, from the decisions shadow
mode recorded in the audit log. This is the data for choosing what to enforce
first (P3-7).

Each would-block is decided again under the policy applied now, so the report
says what enforcement would do today, not only what it would have done: a
decision the current policy no longer blocks is counted apart. Groups are by
the rule that decided, the principal kind and the target, most frequent first,
with the operations in each.

For each group it names the approval that would be needed and its channel
(tty_confirm or out_of_band), and whether that channel is ready:

  out_of_band  needs the daemon and an enrolled passkey, outside a cool-down
               (cerberus approvals enroll)
  tty_confirm  a person at a terminal; for an agent or automation it also needs
               the daemon, which holds the pending approval
  approvers>1  cannot be met yet: fails closed until two people's keys exist
  deny         no approval can allow it

--scope narrows the report to what an enforcement scope would cover, as
comma-separated key=value terms, all of which must match:

  principal=human|agent|automation   env=prod|staging|dev|...|unknown
  effect=read_sensitive|write|...    connector=<id>   target=<resource>
  rule=<rule name>

for example: --scope principal=agent,env=prod`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		since, err := parseAuditTime(policyReportFlags.since)
		if err != nil {
			return fmt.Errorf("--since: %w", err)
		}
		until, err := parseAuditTime(policyReportFlags.until)
		if err != nil {
			return fmt.Errorf("--until: %w", err)
		}
		scope, err := parseReportScope(policyReportFlags.scope)
		if err != nil {
			return fmt.Errorf("--scope: %w", err)
		}
		recs, err := readAuditRecords()
		if err != nil {
			return err
		}
		pdp, source, err := explainPDP(false)
		if err != nil {
			return err
		}
		rep := policyReport(recs, reportOptions{since: since, until: until, scope: scope, pdp: pdp, source: source, channels: currentChannelReadiness(cmd.Context())})
		if policyReportFlags.output == outputFormatJSON {
			return printJSON(rep)
		}
		return writePolicyReport(cmd.OutOrStdout(), rep)
	},
}

// reportScope is --scope: every term must match.
type reportScope map[string]string

var reportScopeKeys = []string{"principal", "env", "effect", "connector", "target", "rule"}

func parseReportScope(raw string) (reportScope, error) {
	scope := reportScope{}
	if strings.TrimSpace(raw) == "" {
		return scope, nil
	}
	for _, term := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(term), "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || value == "" {
			return nil, fmt.Errorf("%q is not key=value; keys are %s", term, strings.Join(reportScopeKeys, ", "))
		}
		known := false
		for _, k := range reportScopeKeys {
			known = known || k == key
		}
		if !known {
			return nil, fmt.Errorf("unknown key %q; keys are %s", key, strings.Join(reportScopeKeys, ", "))
		}
		scope[key] = value
	}
	return scope, nil
}

func (s reportScope) String() string {
	parts := make([]string, 0, len(s))
	for _, k := range reportScopeKeys {
		if v, ok := s[k]; ok {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, ",")
}

func (s reportScope) matches(rec audit.Record, rule string) bool {
	env := rec.Target.Env
	if env == "" {
		env = string(target.EnvUnknown)
	}
	values := map[string]string{"principal": rec.Principal.Kind, "env": env, "effect": rec.Effect, "connector": rec.Connector,
		"target": rec.Target.Resource, "rule": rule}
	for k, want := range s {
		if values[k] != want {
			return false
		}
	}
	return true
}

// channelReadiness is whether each approval channel can be met now.
type channelReadiness struct {
	// Daemon is whether the daemon, which holds pending approvals, answers.
	Daemon bool `json:"daemon"`
	// OutOfBand is whether a passkey can approve: enrolled, no cool-down.
	OutOfBand bool `json:"out_of_band"`
	// Keys is how many passkeys are enrolled, when the daemon said.
	Keys int `json:"keys"`
	// Note says why out of band is not ready, or what is unknown.
	Note string `json:"note,omitempty"`
}

// currentChannelReadiness asks the daemon. A daemon that is not running
// makes both daemon-held channels not ready, and says so.
var currentChannelReadiness = func(ctx context.Context) channelReadiness {
	client, err := newPasskeysClient()
	if err != nil {
		return channelReadiness{Note: "the daemon is not running, so out-of-band readiness is unknown and nothing can hold a pending approval"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := client.PasskeyStatus(ctx)
	if err != nil {
		return channelReadiness{Note: "the daemon did not report its passkeys (" + strings.TrimSpace(err.Error()) + ")"}
	}
	out := channelReadiness{Daemon: true, Keys: len(st.Keys)}
	switch {
	case st.State == presence.StateCooldown:
		out.Note = "the passkey registry is in its cool-down until " + st.CooldownUntil.Local().Format(time.RFC3339)
	case len(st.Keys) == 0:
		out.Note = "no passkey is enrolled; run `cerberus approvals enroll`"
	default:
		out.OutOfBand = true
	}
	return out
}

type reportOptions struct {
	since, until time.Time
	scope        reportScope
	pdp          policy.PDP
	source       string
	channels     channelReadiness
}

// policyReportData is the report.
type policyReportData struct {
	Decided    int              `json:"decided"`
	WouldBlock int              `json:"would_block"`
	StillBlock int              `json:"still_would_block"`
	NoLonger   int              `json:"no_longer_blocked"`
	Scope      string           `json:"scope,omitempty"`
	Policy     string           `json:"policy"`
	Channels   channelReadiness `json:"channels"`
	ByChannel  map[string]int   `json:"by_channel"`
	NotReady   int              `json:"not_ready"`
	Groups     []reportRow      `json:"groups"`
	// Egress is what egress rules did or would have done (P4-4), from the
	// outcome records.
	Egress []egressRow `json:"egress"`
}

// egressRow is one group of egress decisions: one rule, label, action,
// mode, principal kind and target.
type egressRow struct {
	Rule      string    `json:"rule"`
	Label     string    `json:"label"`
	Action    string    `json:"action"`
	Applied   bool      `json:"applied"`
	Principal string    `json:"principal"`
	Target    string    `json:"target"`
	Count     int       `json:"count"`
	Withheld  int       `json:"withheld"`
	Last      time.Time `json:"last"`
}

// reportRow is one group of would-block decisions: one rule, principal kind
// and target, with what enforcing it would need now.
type reportRow struct {
	Rule       string    `json:"rule"`
	Principal  string    `json:"principal"`
	Target     string    `json:"target"`
	Labels     string    `json:"labels"`
	Operations []string  `json:"operations"`
	Recorded   string    `json:"recorded"`
	Now        string    `json:"now"`
	Channel    string    `json:"channel,omitempty"`
	Approvers  int       `json:"approvers,omitempty"`
	Ready      bool      `json:"ready"`
	Need       string    `json:"need"`
	Count      int       `json:"count"`
	Last       time.Time `json:"last"`
}

// policyReport groups the intents whose recorded decision would block, and
// decides each again under the current policy.
func policyReport(recs []audit.Record, o reportOptions) policyReportData {
	rep := policyReportData{Scope: o.scope.String(), Policy: o.source, Channels: o.channels, ByChannel: map[string]int{}, Groups: []reportRow{}}
	groups := map[string]*reportRow{}
	ops := map[string]map[string]bool{}
	for _, rec := range recs {
		if rec.Kind != audit.KindIntent || rec.Policy == nil {
			continue
		}
		if (!o.since.IsZero() && rec.Time.Before(o.since)) || (!o.until.IsZero() && !rec.Time.Before(o.until)) {
			continue
		}
		rule := decidingRule(rec.Policy)
		if !o.scope.matches(rec, rule) {
			continue
		}
		rep.Decided++
		if !rec.Policy.WouldBlock {
			continue
		}
		rep.WouldBlock++
		row := reportRow{Rule: rule, Principal: rec.Principal.Kind, Target: recordTarget(rec.Target), Labels: recordLabels(rec.Target), Recorded: rec.Policy.Decision}
		row.Now, row.Channel, row.Approvers = decideAgain(o.pdp, rec)
		row.Ready, row.Need = readiness(row, o.channels)
		if row.Now == string(policy.Allow) {
			rep.NoLonger++
		} else {
			rep.StillBlock++
			if row.Channel != "" {
				rep.ByChannel[row.Channel]++
			}
			if !row.Ready {
				rep.NotReady++
			}
		}
		key := strings.Join([]string{row.Rule, row.Principal, row.Target, row.Labels, row.Now, row.Channel}, "|")
		g, ok := groups[key]
		if !ok {
			g = &row
			groups[key] = g
			ops[key] = map[string]bool{}
		}
		ops[key][rec.Connector+"."+rec.Operation] = true
		g.Count++
		if rec.Time.After(g.Last) {
			g.Last = rec.Time
		}
	}
	for key, g := range groups {
		for op := range ops[key] {
			g.Operations = append(g.Operations, op)
		}
		sort.Strings(g.Operations)
		rep.Groups = append(rep.Groups, *g)
	}
	rep.Egress = egressSummary(recs, o)
	sort.Slice(rep.Groups, func(i, j int) bool {
		a, b := rep.Groups[i], rep.Groups[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Rule+a.Target < b.Rule+b.Target
	})
	return rep
}

// decideAgain is rec's decision under pdp, and for an approve the channel
// and approvers it would need.
func decideAgain(pdp policy.PDP, rec audit.Record) (decision, channel string, approvers int) {
	if pdp == nil {
		return rec.Policy.Decision, "", 0
	}
	t := recordedTarget(rec)
	res := pdp.Authorize(policy.Request{Connector: rec.Connector, Operation: rec.Operation, Effect: contract.Effect(rec.Effect), Target: t,
		Principal: policy.Principal{Kind: rec.Principal.Kind, Client: rec.Principal.Client, Session: rec.Principal.Session}})
	decision = string(res.Decision)
	if res.Decision == policy.Approve {
		channel, approvers = cerbapi.ApprovalChannel(t, res)
	}
	return decision, channel, approvers
}

// recordedTarget is the target a record names, with its recorded labels.
func recordedTarget(rec audit.Record) target.Target {
	env := target.Env(rec.Target.Env)
	if env == "" {
		env = target.EnvUnknown
	}
	owner, admin := rec.Target.Owner, rec.Target.Admin
	if owner == "" {
		owner = target.OwnerUnknown
	}
	if admin == "" {
		admin = target.AdminUnknown
	}
	return target.Target{Connector: rec.Connector, Kind: rec.Target.Kind, ID: rec.Target.Resource, Resource: rec.Target.Resource,
		Labels:   target.Labels{Env: env, Owner: owner, Admin: target.Admin{Default: admin}, Tags: rec.Target.Tags},
		AdminFor: admin, Adhoc: rec.Target.Adhoc}
}

// readiness is whether the approval row needs can be given now, and what
// it needs in words.
func readiness(row reportRow, ch channelReadiness) (bool, string) {
	switch {
	case row.Now == string(policy.Allow):
		return true, "nothing: the current policy allows it"
	case row.Now == string(policy.Deny):
		return true, "nothing can: policy denies it"
	case row.Approvers > 1:
		return false, fmt.Sprintf("%d approvers: fails closed until that many people's passkeys are enrolled", row.Approvers)
	case row.Channel == approval.ChannelOutOfBand:
		if !ch.OutOfBand {
			return false, "out-of-band approval with a passkey: " + ch.Note
		}
		return true, fmt.Sprintf("out-of-band approval with a passkey (%d enrolled)", ch.Keys)
	case row.Principal == "human":
		return true, "the person's confirmation on their terminal"
	default:
		if !ch.Daemon {
			return false, "an operator's `cerberus approvals approve`, which needs the daemon: " + ch.Note
		}
		return true, "an operator's `cerberus approvals approve` on a terminal"
	}
}

func recordLabels(t audit.Target) string {
	orUnknown := func(v string) string {
		if v == "" {
			return "unknown"
		}
		return v
	}
	return fmt.Sprintf("env %s, owner %s, admin %s", orUnknown(t.Env), orUnknown(t.Owner), orUnknown(t.Admin))
}

// egressSummary groups the outcome records' egress decisions, in the report's
// window and scope (whose rule= term names an egress rule here).
func egressSummary(recs []audit.Record, o reportOptions) []egressRow {
	groups := map[string]*egressRow{}
	for _, rec := range recs {
		if rec.Kind != audit.KindOutcome || len(rec.Egress) == 0 {
			continue
		}
		if (!o.since.IsZero() && rec.Time.Before(o.since)) || (!o.until.IsZero() && !rec.Time.Before(o.until)) {
			continue
		}
		for _, e := range rec.Egress {
			if !o.scope.matches(rec, e.Rule) {
				continue
			}
			row := egressRow{Rule: e.Rule, Label: e.Label, Action: e.Action, Applied: e.Applied, Principal: rec.Principal.Kind, Target: recordTarget(rec.Target)}
			key := fmt.Sprintf("%s|%s|%s|%v|%s|%s", row.Rule, row.Label, row.Action, row.Applied, row.Principal, row.Target)
			g, ok := groups[key]
			if !ok {
				g = &row
				groups[key] = g
			}
			g.Count++
			g.Withheld += e.Withheld
			if rec.Time.After(g.Last) {
				g.Last = rec.Time
			}
		}
	}
	out := make([]egressRow, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// writeEgressSummary is the egress part of the report.
func writeEgressSummary(w io.Writer, rows []egressRow) {
	if len(rows) == 0 {
		return
	}
	shadow := 0
	for _, r := range rows {
		if !r.Applied {
			shadow += r.Count
		}
	}
	fmt.Fprintf(w, "\nEgress: %d decision group(s); %d decision(s) recorded in shadow (would have applied).\n", len(rows), shadow)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "COUNT\tRULE\tLABEL\tACTION\tMODE\tPRINCIPAL\tTARGET\tWITHHELD\tLAST")
	for _, r := range rows {
		mode := "shadow"
		if r.Applied {
			mode = "applied"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", r.Count, r.Rule, r.Label, r.Action, mode, r.Principal, r.Target, r.Withheld, r.Last.UTC().Format(time.RFC3339))
	}
	_ = tw.Flush()
}

func writePolicyReport(w io.Writer, rep policyReportData) error {
	scope := ""
	if rep.Scope != "" {
		scope = " in scope " + rep.Scope
	}
	fmt.Fprintf(w, "%d of %d recorded decision(s)%s would be blocked once enforced.\n", rep.WouldBlock, rep.Decided, scope)
	if rep.Decided == 0 {
		fmt.Fprintln(w, "No shadow decisions are recorded yet. The daemon records one for every gated operation a person or an agent asks for "+
			"(automation, such as plugin loads at start, is not authorized); use Cerberus as usual for a while, then run this again.")
	}
	defer writeEgressSummary(w, rep.Egress)
	defer writeChannels(w, rep)
	if rep.WouldBlock == 0 {
		return nil
	}
	fmt.Fprintf(w, "Under %s: %d still would, %d no longer would.\n", rep.Policy, rep.StillBlock, rep.NoLonger)
	channels := make([]string, 0, len(rep.ByChannel))
	for ch, n := range rep.ByChannel {
		channels = append(channels, fmt.Sprintf("%s %d", ch, n))
	}
	sort.Strings(channels)
	if len(channels) > 0 {
		fmt.Fprintf(w, "Approvals needed: %s.\n", strings.Join(channels, ", "))
	}
	if rep.NotReady > 0 {
		fmt.Fprintf(w, "! %d would-block decision(s) need a channel that is not ready: enforcing them now would refuse them with no way to approve.\n", rep.NotReady)
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer fmt.Fprintln(w)
	fmt.Fprintln(tw, "COUNT\tRULE\tPRINCIPAL\tTARGET\tOPERATIONS\tNOW\tNEEDS\tLAST")
	for _, r := range rep.Groups {
		need := r.Need
		if !r.Ready {
			need = "NOT READY: " + need
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s [%s]\t%s\t%s\t%s\t%s\n", r.Count, orDash(r.Rule), r.Principal, r.Target, r.Labels,
			strings.Join(r.Operations, " "), r.Now, need, r.Last.UTC().Format(time.RFC3339))
	}
	return tw.Flush()
}

func init() {
	policyReportCmd.Flags().StringVar(&policyReportFlags.since, "since", "", "decisions at or after this date or time")
	policyReportCmd.Flags().StringVar(&policyReportFlags.until, "until", "", "decisions before this date or time")
	policyReportCmd.Flags().StringVar(&policyReportFlags.scope, "scope", "", "only decisions in this scope: key=value terms, comma-separated (principal, env, effect, connector, target, rule)")
	addOutputFlag(policyReportCmd, &policyReportFlags.output)
}

// writeChannels says whether each approval channel can be met now, which is
// what enforcing needs whatever the decisions say.
func writeChannels(w io.Writer, rep policyReportData) {
	oob := fmt.Sprintf("out_of_band ready (%d passkey(s) enrolled)", rep.Channels.Keys)
	if !rep.Channels.OutOfBand {
		oob = "out_of_band NOT READY: " + rep.Channels.Note
	}
	daemon := "tty_confirm ready for a person at a terminal, and for agents through the running daemon"
	if !rep.Channels.Daemon {
		daemon = "tty_confirm ready for a person at a terminal only: the daemon is NOT running, so nothing can hold an agent's pending approval"
	}
	fmt.Fprintf(w, "Channels: %s; %s.\n", oob, daemon)
}
