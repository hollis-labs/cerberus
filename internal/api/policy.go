package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The policy and posture routes (slice 7d): `cerberus policy explain|report|apply|enforce` and
// `cerberus posture show|set|reset`, which the old build ran in the CLI's own process against
// ~/.cerberus/policy and the audit log. The serving Cerberus holds both now, so it answers them:
//
//	POST /api/policy/explain             PolicyExplainArgs        PolicyExplain                cerberus.read
//	GET  /api/policy/report?since&until&scope                    PolicyReport                 cerberus.read
//	GET  /api/posture                                             PostureShow                  cerberus.read
//	POST /api/policy/apply[/plan]        PolicyChangeArgs         PolicyChange / PolicyApplied cerberus.admin
//	POST /api/policy/enforce[/plan]      PolicyEnforceArgs        PolicyChange / PolicyApplied cerberus.admin
//	POST /api/posture/set[/plan]         PostureChangeArgs        PolicyChange / PolicyApplied cerberus.admin
//	POST /api/posture/reset[/plan]       PostureChangeArgs        PolicyChange / PolicyApplied cerberus.admin
//
// The plan form answers what old showed before its typed phrase, the phrase, and the snapshot
// hash the change would apply; it changes nothing. The write names that hash (409 plan_stale when
// the files no longer give it), acknowledged (old's phrase), and approval_id. A write is the
// owner's (gate.ApplyOwnerPolicy): the owner on the console, or a key naming the owner's
// out-of-band approval (409 approval_pending first); an MCP client is refused 428. Its record is
// old's: policy apply, effect admin, and for enforce an enforcement_changed record after.
// A change the files cannot make (problems in them, unapplied edits, a snapshot mismatch, the
// posture or enforcement declared in another file) is 409 policy_conflict with old's text.

// PolicyOptions is what the policy and posture routes read and write: the operator's policy
// files (the config dir's policy/, with the audit dir a snapshot that fails its hash check is
// enforced from, and the report reads) and every operation Cerberus can be asked to run, for
// the sampled decisions.
type PolicyOptions struct {
	Store policy.Store
	// Definitions are the connectors' definitions: the admin lane's built-ins and plugins, the
	// local runtime and pipelines, as old's policyDefinitions listed them.
	Definitions func(context.Context) []contract.Definition
}

func (a *API) policyRoutes() []Route {
	rs := []Route{
		{http.MethodPost, "/api/policy/explain", ScopeRead, a.policyExplain},
		{http.MethodGet, "/api/policy/report", ScopeRead, a.policyReport},
		{http.MethodGet, "/api/posture", ScopeRead, a.postureShow},
	}
	for _, form := range []string{"", "/plan"} {
		rs = append(rs,
			Route{http.MethodPost, "/api/policy/apply" + form, ScopeAdmin, a.policyChange(form, a.applyChange)},
			Route{http.MethodPost, "/api/policy/enforce" + form, ScopeAdmin, a.policyChange(form, a.enforceChange)},
			Route{http.MethodPost, "/api/posture/set" + form, ScopeAdmin, a.policyChange(form, a.postureChange(false))},
			Route{http.MethodPost, "/api/posture/reset" + form, ScopeAdmin, a.policyChange(form, a.postureChange(true))},
		)
	}
	return rs
}

// errPolicyConflict marks a change the files cannot make: old's refusals before the phrase.
var errPolicyConflict = errors.New("policy_conflict")

func conflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errPolicyConflict, fmt.Sprintf(format, args...))
}

// policyOpts is the policy store, or false after answering that there is none.
func (a *API) policyOpts(w http.ResponseWriter) (*PolicyOptions, bool) {
	if a.o.Policy == nil {
		apierr.Write(w, http.StatusServiceUnavailable, "policy_unavailable", "this Cerberus serves no policy", nil)
		return nil, false
	}
	return a.o.Policy, true
}

func writePolicyErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPolicyConflict):
		apierr.Write(w, http.StatusConflict, errPolicyConflict.Error(), strings.TrimPrefix(err.Error(), errPolicyConflict.Error()+": "), nil)
	default:
		writeGateErr(w, err)
	}
}

func (a *API) definitions(ctx context.Context) []contract.Definition {
	if a.o.Policy == nil || a.o.Policy.Definitions == nil {
		return nil
	}
	return a.o.Policy.Definitions(ctx)
}

// sampleTargets are the registered resources with their labels (old's policyResources).
func (a *API) sampleTargets() []policy.SampleTarget {
	defs, err := a.o.Resources()
	if err != nil {
		return nil
	}
	out := make([]policy.SampleTarget, 0, len(defs))
	for _, r := range defs {
		out = append(out, policy.SampleTarget{Connector: r.Connector, Labels: r.TargetLabels()})
	}
	return out
}

// appliedPDP is the applied snapshot and how old named it (explainPDP).
func appliedPDP(store policy.Store) (*policy.Evaluator, string) {
	pdp, status := store.LoadVerified()
	source := "the applied snapshot " + status.Snapshot
	switch {
	case status.Snapshot == policy.SnapshotBaseline:
		source = "the built-in baseline (no snapshot is applied)"
	case status.Mismatch():
		source = mismatchSource(status)
	}
	return pdp, source
}

// mismatchSource says what decides on a snapshot mismatch (old's).
func mismatchSource(status policy.LoadStatus) string {
	return "a snapshot mismatch (" + status.Problem + "); " + status.Enforcement
}

// PolicyExplainArgs is `cerberus policy explain`'s request: the operation as
// <connector>.<operation> and old's flags. An empty As is how this server classifies the caller.
type PolicyExplainArgs struct {
	Operation string `json:"operation"`
	Target    string `json:"target,omitempty"`
	As        string `json:"as,omitempty"`
	DryRun    bool   `json:"dry_run,omitempty"`
	Adhoc     bool   `json:"adhoc,omitempty"`
	Working   bool   `json:"working,omitempty"`
}

// PolicyExplain is old's `policy explain -o json` map, plus what its text printed beside it
// (Text), which the CLI's JSON leaves out.
type PolicyExplain struct {
	Request        policy.Request     `json:"request"`
	Result         policy.Result      `json:"result"`
	Policy         string             `json:"policy"`
	Posture        string             `json:"posture"`
	PostureRules   []int              `json:"posture_rules"`
	GrantWarnings  []string           `json:"grant_warnings"`
	EgressWarnings []string           `json:"egress_warnings"`
	BreakGlass     policy.BreakGlass  `json:"break_glass"`
	Text           *PolicyExplainText `json:"text,omitempty"`
}

// PolicyExplainText is the rest of old's text: enforcement, brakes, credentials and the
// circuit breaker.
type PolicyExplainText struct {
	// Enforcement is "policy" (never, for policy changes), "enforced" or "shadow"; empty when
	// the decision point says nothing about enforcement.
	Enforcement     string `json:"enforcement,omitempty"`
	EnforcedBy      string `json:"enforced_by,omitempty"`
	EnforcementNote string `json:"enforcement_note,omitempty"`
	// Brakes is old's brakes line, without its leading blank line; empty when none applies.
	Brakes string `json:"brakes,omitempty"`
	// Credentials are the connector's declared secrets' bindings; CredentialsError is
	// connector-secrets.yaml failing to load. Neither when it declares none.
	Credentials      []admin.CredentialBindingView `json:"credentials,omitempty"`
	CredentialsError string                        `json:"credentials_error,omitempty"`
	CircuitBreaker   string                        `json:"circuit_breaker,omitempty"`
}

func (a *API) policyExplain(w http.ResponseWriter, r *http.Request) {
	opts, ok := a.policyOpts(w)
	if !ok {
		return
	}
	var args PolicyExplainArgs
	if err := decodeBody(r.Body, &args); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
		return
	}
	out, err := a.explain(GateContext(r.Context()), opts, args)
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
		return
	}
	writeJSON(w, out)
}

func (a *API) explain(ctx context.Context, opts *PolicyOptions, args PolicyExplainArgs) (*PolicyExplain, error) {
	connectorID, operation, ok := strings.Cut(args.Operation, ".")
	if !ok || connectorID == "" || operation == "" {
		return nil, errors.New("name the operation as <connector>.<operation>, for example local.deploy or kubernetes.delete_pod")
	}
	defs := a.definitions(ctx)
	var def *contract.Definition
	for i := range defs {
		if defs[i].ID == connectorID {
			def = &defs[i]
		}
	}
	if def == nil {
		return nil, fmt.Errorf("unknown connector %q", connectorID)
	}
	op, known := def.Operation(operation)
	if !known {
		return nil, fmt.Errorf("connector %q has no operation %q", connectorID, operation)
	}
	op = op.Finalize()
	kind := args.As
	if kind == "" {
		who, _ := gate.PrincipalFrom(ctx)
		kind = string(who.Kind)
	}
	if kind != "human" && kind != "agent" && kind != "automation" {
		return nil, errors.New("--as takes human, agent or automation")
	}
	var labels *target.ResourceLabels
	if args.Target != "" {
		for _, s := range a.sampleTargets() {
			if s.Labels.ID == args.Target {
				l := s.Labels
				labels = &l
			}
		}
		if labels == nil {
			return nil, fmt.Errorf("resource %q is not in the config; run `cerberus resource list`, or pass --adhoc for a target named by connection settings", args.Target)
		}
	}
	t := target.Resolve(connectorID, op.Target.Kind, args.Target, labels, args.Adhoc)
	req := policy.Request{Connector: connectorID, Operation: operation, Effect: op.PolicyEffect(), EffectUndeclared: op.EffectUndeclared, Target: t,
		DryRun: args.DryRun && op.Preview != contract.PreviewNone, Principal: policy.Principal{Kind: kind}}
	var pdp policy.PDP
	var source string
	if args.Working {
		f, problems, err := opts.Store.LoadWorking()
		if err != nil {
			return nil, err
		}
		if len(problems) > 0 {
			return nil, fmt.Errorf("the working policy files have problems:\n  %s", strings.Join(problems, "\n  "))
		}
		pdp, source = policy.NewEvaluator(f, "working"), "the working files (not applied)"
	} else {
		pdp, source = appliedPDP(opts.Store)
	}
	res := pdp.Authorize(req)
	out := &PolicyExplain{Request: req, Result: res, Policy: source, Posture: res.Posture, BreakGlass: policy.BreakGlassLimitsOf(pdp), Text: &PolicyExplainText{}}
	if file, ok := policy.FileOf(pdp); ok {
		_, out.PostureRules = file.PostureFor(req)
		out.GrantWarnings = file.GrantWarnings()
		out.EgressWarnings = file.EgressWarnings()
	}
	if e, ok := pdp.(interface {
		Enforced(policy.Request) (bool, string)
		Enforcement() (policy.Enforcement, string)
	}); ok {
		on, by := e.Enforced(req)
		_, out.Text.EnforcementNote = e.Enforcement()
		switch {
		case connectorID == "policy":
			out.Text.Enforcement = "policy"
		case on:
			out.Text.Enforcement, out.Text.EnforcedBy = "enforced", by
		default:
			out.Text.Enforcement = "shadow"
		}
	}
	out.Text.Brakes = a.explainBrakes(connectorID, req)
	if len(def.Config.Secrets) > 0 && a.o.Admin != nil {
		views, err := a.o.Admin.ExplainCredentials(*def, operation, req.DryRun, t)
		if err != nil {
			out.Text.CredentialsError = err.Error()
		}
		out.Text.Credentials = views
	}
	if cb := policy.CircuitBreakerOf(pdp); cb != nil {
		out.Text.CircuitBreaker = cb.String()
	}
	return out, nil
}

// explainBrakes is old's writeExplainBrakes line: the brake that would refuse the call. The
// brakes run before policy, so it outranks the decision.
func (a *API) explainBrakes(connectorID string, req policy.Request) string {
	if gate.Unbraked(connectorID) || a.o.Gate == nil || a.o.Gate.Brakes() == nil {
		return ""
	}
	st := a.o.Gate.Brakes().Current()
	blocked, l, f := st.Blocks(connectorID, req.Effect, req.Target)
	switch {
	case !blocked || req.DryRun:
		if st.Engaged() && !req.DryRun {
			return "Brakes: engaged, and this call passes them."
		}
		return ""
	case l != nil:
		return fmt.Sprintf("Brakes: REFUSED by lockdown %s%s, whatever the policy says. Lift: cerberus lockdown --off", l.ID, brakeReason(l.Reason))
	default:
		return fmt.Sprintf("Brakes: REFUSED by freeze %s on %s%s, whatever the policy says. Lift: cerberus freeze --off %s", f.ID, f.Match.String(), brakeReason(f.Reason), f.ID)
	}
}

func brakeReason(reason string) string {
	if reason == "" {
		return ""
	}
	return fmt.Sprintf(" (%q)", reason)
}

// ReportScope is `policy report --scope` and `policy enforce --scope`: every term must match.
type ReportScope map[string]string

// ReportScopeKeys are the scope's keys, in old's order.
var ReportScopeKeys = []string{"principal", "env", "effect", "connector", "target", "rule"}

// ParseReportScope reads key=value terms, comma-separated (old's parseReportScope).
func ParseReportScope(raw string) (ReportScope, error) {
	scope := ReportScope{}
	if strings.TrimSpace(raw) == "" {
		return scope, nil
	}
	for _, term := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(term), "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || value == "" {
			return nil, fmt.Errorf("%q is not key=value; keys are %s", term, strings.Join(ReportScopeKeys, ", "))
		}
		known := false
		for _, k := range ReportScopeKeys {
			known = known || k == key
		}
		if !known {
			return nil, fmt.Errorf("unknown key %q; keys are %s", key, strings.Join(ReportScopeKeys, ", "))
		}
		scope[key] = value
	}
	return scope, nil
}

func (s ReportScope) String() string {
	parts := make([]string, 0, len(s))
	for _, k := range ReportScopeKeys {
		if v, ok := s[k]; ok {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, ",")
}

func (s ReportScope) matches(rec audit.Record, rule string) bool {
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

// ChannelReadiness is old's channel readiness. Passkeys are gone (decision 0.1): an approval is
// met by the owner on the console, which the serving Cerberus, answering, holds; so both channels
// are ready, and Keys is always 0.
type ChannelReadiness struct {
	Daemon    bool   `json:"daemon"`
	OutOfBand bool   `json:"out_of_band"`
	Keys      int    `json:"keys"`
	Note      string `json:"note,omitempty"`
}

// consoleChannels is the readiness the serving Cerberus answers with.
var consoleChannels = ChannelReadiness{Daemon: true, OutOfBand: true,
	Note: "the owner approves on the console, signed in; the serving Cerberus holds the pending approvals"}

// PolicyReport is old's report.
type PolicyReport struct {
	Decided    int              `json:"decided"`
	WouldBlock int              `json:"would_block"`
	StillBlock int              `json:"still_would_block"`
	NoLonger   int              `json:"no_longer_blocked"`
	Scope      string           `json:"scope,omitempty"`
	Policy     string           `json:"policy"`
	Channels   ChannelReadiness `json:"channels"`
	ByChannel  map[string]int   `json:"by_channel"`
	NotReady   int              `json:"not_ready"`
	Groups     []ReportRow      `json:"groups"`
	Egress     []EgressRow      `json:"egress"`
}

// EgressRow is one group of egress decisions.
type EgressRow struct {
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

// ReportRow is one group of would-block decisions.
type ReportRow struct {
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

type reportOptions struct {
	since, until time.Time
	scope        ReportScope
	pdp          policy.PDP
	source       string
}

func (a *API) policyReport(w http.ResponseWriter, r *http.Request) {
	opts, ok := a.policyOpts(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	var o reportOptions
	var err error
	if o.since, err = parseReportTime(q.Get("since")); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", "--since: "+err.Error(), nil)
		return
	}
	if o.until, err = parseReportTime(q.Get("until")); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", "--until: "+err.Error(), nil)
		return
	}
	if o.scope, err = ParseReportScope(q.Get("scope")); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", "--scope: "+err.Error(), nil)
		return
	}
	recs, err := audit.ReadRecords(opts.Store.AuditDir)
	if err != nil {
		writeErr(w, err)
		return
	}
	o.pdp, o.source = appliedPDP(opts.Store)
	writeJSON(w, buildReport(recs, o))
}

// parseReportTime reads RFC 3339, the form the CLI sends; empty is no bound.
func parseReportTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// buildReport is old's policyReport: the intents whose recorded decision would block, each
// decided again under the current policy, grouped.
func buildReport(recs []audit.Record, o reportOptions) PolicyReport {
	rep := PolicyReport{Scope: o.scope.String(), Policy: o.source, Channels: consoleChannels, ByChannel: map[string]int{}, Groups: []ReportRow{}}
	groups := map[string]*ReportRow{}
	ops := map[string]map[string]bool{}
	for _, rec := range recs {
		if rec.Kind != audit.KindIntent || rec.Policy == nil {
			continue
		}
		if (!o.since.IsZero() && rec.Time.Before(o.since)) || (!o.until.IsZero() && !rec.Time.Before(o.until)) {
			continue
		}
		rule := DecidingRule(rec.Policy)
		if !o.scope.matches(rec, rule) {
			continue
		}
		rep.Decided++
		if !rec.Policy.WouldBlock {
			continue
		}
		rep.WouldBlock++
		row := ReportRow{Rule: rule, Principal: rec.Principal.Kind, Target: RecordTarget(rec.Target), Labels: recordLabels(rec.Target), Recorded: rec.Policy.Decision}
		row.Now, row.Channel, row.Approvers = decideAgain(o.pdp, rec)
		row.Ready, row.Need = readiness(row)
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
		x, y := rep.Groups[i], rep.Groups[j]
		if x.Count != y.Count {
			return x.Count > y.Count
		}
		return x.Rule+x.Target < y.Rule+y.Target
	})
	return rep
}

// DecidingRule is the first matched rule with the decision's own value.
func DecidingRule(p *audit.PolicyDecision) string {
	for _, m := range p.MatchedRules {
		if m.Decision == p.Decision {
			return m.Rule
		}
	}
	return ""
}

// RecordTarget is a recorded target as the report names it.
func RecordTarget(t audit.Target) string {
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

func decideAgain(pdp policy.PDP, rec audit.Record) (decision, channel string, approvers int) {
	if pdp == nil {
		return rec.Policy.Decision, "", 0
	}
	t := recordedTarget(rec)
	res := pdp.Authorize(policy.Request{Connector: rec.Connector, Operation: rec.Operation, Effect: contract.Effect(rec.Effect), Target: t,
		Principal: policy.Principal{Kind: rec.Principal.Kind, Client: rec.Principal.Client, Session: rec.Principal.Session}})
	decision = string(res.Decision)
	if res.Decision == policy.Approve {
		channel, approvers = gate.ApprovalChannel(t, res)
	}
	return decision, channel, approvers
}

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

// readiness is old's, for the console: an approve is met by the owner on the console, so it is
// always ready; more approvers than one cannot be met (one owner).
func readiness(row ReportRow) (bool, string) {
	switch {
	case row.Now == string(policy.Allow):
		return true, "nothing: the current policy allows it"
	case row.Now == string(policy.Deny):
		return true, "nothing can: policy denies it"
	case row.Approvers > 1:
		return false, fmt.Sprintf("%d approvers: fails closed, as only the owner approves (decision 0.1)", row.Approvers)
	case row.Channel == approval.ChannelOutOfBand:
		return true, "out-of-band approval by the owner on the console"
	default:
		return true, "the owner's approval on the console"
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

func egressSummary(recs []audit.Record, o reportOptions) []EgressRow {
	groups := map[string]*EgressRow{}
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
			row := EgressRow{Rule: e.Rule, Label: e.Label, Action: e.Action, Applied: e.Applied, Principal: rec.Principal.Kind, Target: RecordTarget(rec.Target)}
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
	out := make([]EgressRow, 0, len(groups))
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

// HostWideRow is one host-wide switch the global posture governs (section 13's table, old's
// hostWide). Two rows say what this build does: the owner accepts a plugin review on the console
// whatever the posture (decision 0.1: old's --yes is obsolete), and MCP over HTTP is /mcp on the
// serving Cerberus behind its guard (old's mcp-http --insecure-listen is gone).
type HostWideRow struct {
	Switch     string `json:"switch"`
	Secure     string `json:"secure"`
	Permissive string `json:"permissive"`
	Live       bool   `json:"live"`
}

// HostWide is the table.
var HostWide = []HostWideRow{
	{Switch: "plugin install", Secure: "the owner accepts the review on the console", Permissive: "the same: the owner accepts the review on the console (decision 0.1)", Live: true},
	{Switch: "changed plugin", Secure: "refused until re-reviewed", Permissive: "loaded, with a warning and an audit record", Live: true},
	{Switch: "MCP exposure of plugin operations", Secure: "only what connector-config.yaml exposes", Permissive: "every declared operation, narrowed by connector-config.yaml", Live: true},
	{Switch: "MCP over HTTP", Secure: "/mcp on the serving Cerberus, behind its auth guard", Permissive: "the same: the posture does not open it", Live: true},
}

// HostWideRows is the table with the side global selects (old's hostWideRows).
func HostWideRows(global string) []map[string]any {
	out := make([]map[string]any, 0, len(HostWide))
	for _, r := range HostWide {
		effect := r.Secure
		if global == policy.PosturePermissive {
			effect = r.Permissive
		}
		out = append(out, map[string]any{"switch": r.Switch, "now": effect, "live": r.Live})
	}
	return out
}

// PostureShow is old's `posture show -o json`.
type PostureShow struct {
	Posture  policy.PostureSummary `json:"posture"`
	HostWide []map[string]any      `json:"host_wide"`
}

func (a *API) postureShow(w http.ResponseWriter, _ *http.Request) {
	opts, ok := a.policyOpts(w)
	if !ok {
		return
	}
	summary := opts.Store.CurrentPosture()
	ev, _ := opts.Store.LoadVerified()
	if _, note := ev.Enforcement(); note != "" {
		summary.Enforcement = note
	}
	writeJSON(w, PostureShow{Posture: summary, HostWide: HostWideRows(summary.Global)})
}

// PolicyChangeArgs are a write's form fields: the snapshot hash the plan showed, the
// acknowledgment (old's typed phrase) and the owner's approval.
type PolicyChangeArgs struct {
	Hash         string `json:"hash,omitempty"`
	Acknowledged bool   `json:"acknowledged,omitempty"`
	ApprovalID   string `json:"approval_id,omitempty"`
}

// PolicyEnforceArgs is `policy enforce`'s request.
type PolicyEnforceArgs struct {
	PolicyChangeArgs
	Scope string        `json:"scope,omitempty"`
	ID    string        `json:"id,omitempty"`
	All   bool          `json:"all,omitempty"`
	Off   bool          `json:"off,omitempty"`
	Since time.Duration `json:"since,omitempty"`
}

// PostureChangeArgs is `posture set` (Posture) and `posture reset`'s request.
type PostureChangeArgs struct {
	PolicyChangeArgs
	Posture string   `json:"posture,omitempty"`
	Scope   []string `json:"scope,omitempty"`
}

// PolicyChange is what a change would do, as old showed it before the phrase. Nothing is set
// (with the message old printed) when there is nothing to change.
type PolicyChange struct {
	Nothing string `json:"nothing,omitempty"`
	// Hash is the snapshot the change applies; Over what it applies over.
	Hash string `json:"hash"`
	Over string `json:"over,omitempty"`
	// What and Phrase are old's "This will <what>. Type <phrase>".
	What   string        `json:"what,omitempty"`
	Phrase string        `json:"phrase"`
	Flips  []policy.Flip `json:"flips"`
	// FlipsShown says whether old showed the flips (enforce did not).
	FlipsShown     bool     `json:"flips_shown"`
	GrantWarnings  []string `json:"grant_warnings,omitempty"`
	EgressWarnings []string `json:"egress_warnings,omitempty"`
	// Enforcement before and after (enforce), with the scope's readiness unless --off.
	EnforcementBefore string        `json:"enforcement_before,omitempty"`
	EnforcementAfter  string        `json:"enforcement_after,omitempty"`
	Readiness         *PolicyReport `json:"readiness,omitempty"`
	Since             time.Duration `json:"since,omitempty"`
	// Posture before and after (set, reset).
	PostureBefore *policy.PostureSummary `json:"posture_before,omitempty"`
	PostureAfter  *policy.PostureSummary `json:"posture_after,omitempty"`
}

// PolicyApplied is what a write applied.
type PolicyApplied struct {
	Applied     string                 `json:"applied"`
	Proof       string                 `json:"proof"`
	Enforcement string                 `json:"enforcement,omitempty"`
	Posture     *policy.PostureSummary `json:"posture,omitempty"`
}

// pendingChange is a change computed from the files, ready to apply: the working file, how many
// sampled decisions flip, and what to write first and how to undo it.
type pendingChange struct {
	view    PolicyChange
	working policy.File
	flips   int
	write   func() (restore func(), err error)
	after   func(ctx context.Context, g *gate.Gate) error
	applied func(PolicyApplied) PolicyApplied
}

// changeFunc computes a change from the request body.
type changeFunc func(ctx context.Context, store policy.Store, body []byte) (*pendingChange, PolicyChangeArgs, error)

// policyChange serves one change's plan and write forms.
func (a *API) policyChange(form string, compute changeFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts, ok := a.policyOpts(w)
		if !ok {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		ctx := GateContext(r.Context())
		change, args, err := compute(ctx, opts.Store, body)
		if err != nil {
			writePolicyErr(w, err)
			return
		}
		if form == "/plan" || change.view.Nothing != "" {
			writeJSON(w, change.view)
			return
		}
		if a.o.Gate == nil {
			apierr.Write(w, http.StatusServiceUnavailable, "policy_unavailable",
				"this Cerberus changes no policy: in observe mode the old Cerberus applies it until the handover", nil)
			return
		}
		if args.Hash != change.view.Hash {
			writeGateErr(w, gate.NewError("policy", "apply", gate.CodePlanStale,
				fmt.Errorf("the change now applies %s, not the %s you were shown: the policy files changed; run it again to see the change", shortHash(change.view.Hash), shortHash(args.Hash))))
			return
		}
		applied, proof, err := a.o.Gate.ApplyOwnerPolicy(ctx, opts.Store, change.working, change.flips,
			gate.OwnerPolicyArgs{Acknowledged: args.Acknowledged, ApprovalID: args.ApprovalID, What: changeWhat(change.view), Prepare: change.write})
		if err != nil {
			writeGateErr(w, err)
			return
		}
		out := PolicyApplied{Applied: applied, Proof: proof}
		if change.after != nil {
			if err = change.after(ctx, a.o.Gate); err != nil {
				writeErr(w, fmt.Errorf("applied %s, but the enforcement change could not be recorded: %w", applied, err))
				return
			}
		}
		if change.applied != nil {
			out = change.applied(out)
		}
		writeJSON(w, out)
	}
}

func changeWhat(v PolicyChange) string {
	if v.What != "" {
		return "a policy change (" + v.What + ")"
	}
	return "applying the policy snapshot " + shortHash(v.Hash)
}

// ShortHash is old's shortHash: the first 12 hex digits.
func ShortHash(h string) string { return shortHash(h) }

func shortHash(h string) string {
	h = strings.TrimPrefix(h, "sha256:")
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// applyChange is `policy apply`: the working files as the applied snapshot.
func (a *API) applyChange(ctx context.Context, store policy.Store, body []byte) (*pendingChange, PolicyChangeArgs, error) {
	var args PolicyChangeArgs
	if err := decodeJSON(body, &args); err != nil {
		return nil, args, err
	}
	working, problems, err := store.LoadWorking()
	if err != nil {
		return nil, args, err
	}
	if len(problems) > 0 {
		return nil, args, conflict("the working policy files have problems, so nothing was applied:\n  %s", strings.Join(problems, "\n  "))
	}
	current, status := store.LoadVerified()
	data, err := policy.Encode(working)
	if err != nil {
		return nil, args, err
	}
	hash := policy.Hash(data)
	view := PolicyChange{Hash: hash, Phrase: "apply " + shortHash(hash), FlipsShown: true}
	if status.Snapshot == hash {
		view.Nothing = "The working files are already the applied snapshot; nothing to apply."
		return &pendingChange{view: view}, args, nil
	}
	flips := policy.Flips(current, policy.NewEvaluator(working, hash), policy.Sample(a.definitions(ctx), a.sampleTargets()))
	view.Over = status.Snapshot
	if status.Mismatch() {
		view.Over = mismatchSource(status)
	}
	view.Flips, view.GrantWarnings, view.EgressWarnings = flips, working.GrantWarnings(), working.EgressWarnings()
	return &pendingChange{view: view, working: working, flips: len(flips)}, args, nil
}

// enforceEntryFromScope is a report scope as an enforced scope (old's).
func enforceEntryFromScope(scope ReportScope, id string) (policy.EnforceEntry, error) {
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

// workingDiffersFromApplied is old's: whether the working files would apply something other
// than the applied snapshot.
func workingDiffersFromApplied(store policy.Store, applied *policy.Evaluator) (bool, error) {
	working, problems, err := store.LoadWorking()
	if err != nil || len(problems) > 0 {
		return false, err
	}
	w, err := policy.Encode(working)
	if err != nil {
		return false, err
	}
	ap, err := policy.Encode(applied.File())
	if err != nil {
		return false, err
	}
	return policy.Hash(w) != policy.Hash(ap), nil
}

// enforceChange is `policy enforce`.
func (a *API) enforceChange(_ context.Context, store policy.Store, body []byte) (*pendingChange, PolicyChangeArgs, error) {
	var f PolicyEnforceArgs
	if err := decodeJSON(body, &f); err != nil {
		return nil, f.PolicyChangeArgs, err
	}
	if (f.Scope == "") == !f.All {
		return nil, f.PolicyChangeArgs, invalid("name what to enforce: --scope key=value,... for a scope, or --all for everything")
	}
	scope, err := ParseReportScope(f.Scope)
	if err != nil {
		return nil, f.PolicyChangeArgs, invalid("--scope: %v", err)
	}
	entry, err := enforceEntryFromScope(scope, f.ID)
	if err != nil {
		return nil, f.PolicyChangeArgs, invalid("%v", err)
	}
	if elsewhere, eerr := store.EnforcementDeclaredElsewhere(); eerr != nil {
		return nil, f.PolicyChangeArgs, eerr
	} else if len(elsewhere) > 0 {
		return nil, f.PolicyChangeArgs, conflict("enforcement is also declared in %s; `cerberus policy enforce` owns it in %s, so move it there or remove it, then retry", strings.Join(elsewhere, ", "), policy.EnforcementFileName)
	}
	current, status := store.LoadVerified()
	if status.Mismatch() {
		return nil, f.PolicyChangeArgs, conflict("the applied policy snapshot is a mismatch (%s), and %s; review the working files and run `cerberus policy apply` before changing enforcement", status.Problem, strings.TrimPrefix(status.Enforcement, "snapshot mismatch: "))
	}
	if unapplied, uerr := workingDiffersFromApplied(store, current); uerr != nil {
		return nil, f.PolicyChangeArgs, uerr
	} else if unapplied {
		return nil, f.PolicyChangeArgs, conflict("the working policy files have changes that are not applied; review and apply them with `cerberus policy apply` first, so an enforcement change does not carry them in unseen")
	}
	file, err := store.EnforcementFile()
	if err != nil {
		return nil, f.PolicyChangeArgs, fmt.Errorf("read %s: %w", policy.EnforcementFileName, err)
	}
	before := current.File().EnforcementOf()
	next := file.EnforcementOf()
	var what, phrase string
	switch {
	case f.All && f.Off:
		next.Mode, what, phrase = policy.EnforceShadow, "return everything to shadow except the enforced scopes", "shadow everything"
	case f.All:
		next.Mode, what, phrase = policy.EnforceAll, "enforce everything", "enforce everything"
	case f.Off:
		if !next.RemoveEnforceEntry(entry) {
			return nil, f.PolicyChangeArgs, conflict("no enforced scope is %s; `cerberus posture show` lists them", scope)
		}
		what, phrase = "return "+scope.String()+" to shadow", "shadow "+scope.String()
	default:
		next.SetEnforceEntry(entry)
		what, phrase = "enforce "+scope.String(), "enforce "+scope.String()
	}
	file.Enforcement = &next
	working, problems, err := store.LoadWorkingWithEnforcement(file)
	if err != nil {
		return nil, f.PolicyChangeArgs, err
	}
	if len(problems) > 0 {
		return nil, f.PolicyChangeArgs, conflict("the working policy files have problems, so nothing was changed:\n  %s", strings.Join(problems, "\n  "))
	}
	after := working.EnforcementOf()
	data, err := policy.Encode(working)
	if err != nil {
		return nil, f.PolicyChangeArgs, err
	}
	hash := policy.Hash(data)
	view := PolicyChange{Hash: hash, What: what, Phrase: phrase, EnforcementBefore: before.Summary(), EnforcementAfter: after.Summary(), Since: f.Since}
	if hash == status.Snapshot {
		view.Nothing = "That is already the applied enforcement; nothing to change."
		return &pendingChange{view: view}, f.PolicyChangeArgs, nil
	}
	if !f.Off {
		recs, rerr := audit.ReadRecords(store.AuditDir)
		if rerr != nil {
			return nil, f.PolicyChangeArgs, rerr
		}
		rep := buildReport(recs, reportOptions{since: time.Now().Add(-f.Since), scope: scope, pdp: current, source: "the applied snapshot " + status.Snapshot})
		view.Readiness = &rep
	}
	return &pendingChange{view: view, working: working,
		write: func() (func(), error) { return store.WriteEnforcementFile(file) },
		after: func(ctx context.Context, g *gate.Gate) error { return g.RecordEnforcementChange(ctx, before, after) },
		applied: func(p PolicyApplied) PolicyApplied {
			p.Enforcement = after.Summary()
			return p
		}}, f.PolicyChangeArgs, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBadArgs, fmt.Sprintf(format, args...))
}

// parsePostureScope is old's parseScope: --scope key=value pairs as a match, or nil for none.
func parsePostureScope(pairs []string) (*policy.TargetMatch, error) {
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

// postureChange is `posture set` and, with reset, `posture reset` (old's changePosture).
func (a *API) postureChange(reset bool) changeFunc {
	return func(ctx context.Context, store policy.Store, body []byte) (*pendingChange, PolicyChangeArgs, error) {
		var args PostureChangeArgs
		if err := decodeJSON(body, &args); err != nil {
			return nil, args.PolicyChangeArgs, err
		}
		if !reset && args.Posture != policy.PostureSecure && args.Posture != policy.PosturePermissive {
			return nil, args.PolicyChangeArgs, invalid("the posture is secure or permissive, not %q", args.Posture)
		}
		match, err := parsePostureScope(args.Scope)
		if err != nil {
			return nil, args.PolicyChangeArgs, invalid("%v", err)
		}
		elsewhere, err := store.PostureDeclaredElsewhere()
		if err != nil {
			return nil, args.PolicyChangeArgs, err
		}
		if len(elsewhere) > 0 {
			return nil, args.PolicyChangeArgs, conflict("the posture is also declared in %s; `cerberus posture` owns it in posture.yaml, so move it there or remove it, then retry", strings.Join(elsewhere, ", "))
		}
		current, status := store.LoadVerified()
		if status.Mismatch() {
			return nil, args.PolicyChangeArgs, conflict("the applied policy snapshot is a mismatch (%s), and %s; review the working files and run `cerberus policy apply` before changing the posture", status.Problem, strings.TrimPrefix(status.Enforcement, "snapshot mismatch: "))
		}
		if unapplied, uerr := workingDiffersFromApplied(store, current); uerr != nil {
			return nil, args.PolicyChangeArgs, uerr
		} else if unapplied {
			return nil, args.PolicyChangeArgs, conflict("the working policy files have changes that are not applied; review and apply them with `cerberus policy apply` first, so a posture change does not carry them in unseen")
		}
		file, err := store.PostureFile()
		if err != nil {
			return nil, args.PolicyChangeArgs, fmt.Errorf("read %s: %w", policy.PostureFileName, err)
		}
		before := file.PostureSummary(status.Snapshot)
		var what string
		switch {
		case reset && match == nil:
			file = policy.File{Version: policy.FileVersion}
			what = "reset the posture to secure, with no posture rules"
		case reset:
			if !file.RemovePostureRule(*match) {
				return nil, args.PolicyChangeArgs, conflict("there is no posture rule for %s; `cerberus posture show` lists them", match.String())
			}
			what = "remove the posture rule for " + match.String()
		case match == nil:
			file.Posture = args.Posture
			what = "set the global posture to " + args.Posture
		default:
			file.SetPostureRule(*match, args.Posture)
			what = fmt.Sprintf("set %s for %s", args.Posture, match.String())
		}
		working, problems, err := store.LoadWorkingWithPosture(file)
		if err != nil {
			return nil, args.PolicyChangeArgs, err
		}
		if len(problems) > 0 {
			return nil, args.PolicyChangeArgs, conflict("the working policy files have problems, so nothing was changed:\n  %s", strings.Join(problems, "\n  "))
		}
		data, err := policy.Encode(working)
		if err != nil {
			return nil, args.PolicyChangeArgs, err
		}
		hash := policy.Hash(data)
		view := PolicyChange{Hash: hash, What: what, Phrase: "posture " + shortHash(hash), PostureBefore: &before, FlipsShown: true}
		if hash == status.Snapshot {
			view.Nothing = "That is already the applied posture; nothing to change."
			return &pendingChange{view: view}, args.PolicyChangeArgs, nil
		}
		after := working.PostureSummary(hash)
		view.PostureAfter = &after
		flips := policy.Flips(current, policy.NewEvaluator(working, hash), policy.Sample(a.definitions(ctx), a.sampleTargets()))
		view.Flips = flips
		return &pendingChange{view: view, working: working, flips: len(flips),
			write: func() (func(), error) { return store.WritePostureFile(file) },
			applied: func(p PolicyApplied) PolicyApplied {
				p.Posture = &after
				return p
			}}, args.PolicyChangeArgs, nil
	}
}

// decodeJSON decodes a change's body; empty is the zero value.
func decodeJSON(body []byte, dst any) error {
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return invalid("decode body: %v", err)
	}
	return nil
}
