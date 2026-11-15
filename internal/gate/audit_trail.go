package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// ResourceLookup returns the configured resource with the given id, from
// whichever config the serving process resolved.
type ResourceLookup func(id string) (*config.ResourceDef, bool)

// ConfigResourceLookup looks resources up in a fixed, already-resolved config.
func ConfigResourceLookup(cfg *config.ConfigV2) ResourceLookup {
	return func(id string) (*config.ResourceDef, bool) {
		if cfg == nil {
			return nil, false
		}
		for i := range cfg.Resources {
			if cfg.Resources[i].ID == id {
				return &cfg.Resources[i], true
			}
		}
		return nil, false
	}
}

// BreakGlassRequest is a person breaking glass on their own call (P3-5b):
// the reason, and the target as they typed it.
type BreakGlassRequest struct {
	Reason string `json:"reason"`
	Typed  string `json:"typed"`
}

// Spec is what an operation's records say about it (the old build's
// auditSpec). It is built from the request and the operation's contract,
// never from its result, so the intent can be written before anything runs.
type Spec struct {
	Connector string
	Operation string
	// Op is the operation's contract, and Known whether one was found. An
	// operation with no contract is treated as needing its record, like any
	// other gap: fail closed.
	Op           contract.Operation
	Known        bool
	Config       map[string]any
	Acknowledged bool
	DryRun       bool
	// Credentials are the secret names the connector resolves, as
	// "<connector>/<name>". They are recorded on an outcome only when the
	// call got as far as resolving them.
	Credentials []string
	// Automation marks Cerberus acting on its own. Its record is written but
	// never gates the action: an unwritable log is logged, not a refusal.
	Automation bool
	// AutomationVia names which part of Cerberus acted, for an automation
	// record that is not the monitor's: daemon_start for a plugin restored
	// when the daemon starts.
	AutomationVia string
	Reason        string
	// For a loaded plugin: its config and entrypoint fingerprints.
	PluginConfigSHA256     string
	PluginEntrypointSHA256 string
	// Resources resolves a target's registered resource, for its labels.
	Resources ResourceLookup
	// ResolvedTarget is supplied by lanes with an authoritative declaration
	// outside the local resource namespace (for example a remote Host).
	ResolvedTarget *target.Target
	// Preview is plugin_claimed for a dry run a plugin serves itself.
	Preview string
	// Review is an install review's record.
	Review *audit.PluginReview
	// ApprovalID is the approval the call runs under, and Plan computes the
	// plan an approval binds to (P3-2): the lane's one plan function, used
	// when the approval is asked for and when it is used.
	ApprovalID string
	// PlanOnly marks a request for the plan alone (connectors plan).
	PlanOnly bool
	// OwnerAuthorized marks an operation only the owner authorizes (OwnerApproval, the lead's
	// ruling (a) on decision 0.1): secrets set, a plugin install, upgrade or review. A policy
	// approve on it is met by the owner's out-of-band approval the lane asks for with
	// Call.OwnerApproval: one approval, bound to the same plan and consumed once, never a
	// second policy approval beside it.
	OwnerAuthorized bool
	// ConfirmedPlanHash is the plan a person confirmed on their own
	// terminal (P3-3), from a confirm route only.
	ConfirmedPlanHash string
	// BreakGlass is a person breaking glass on their own call (P3-5b),
	// from a break-glass route only.
	BreakGlass *BreakGlassRequest
	// Enforcement is a policy apply's enforcement section, recorded on its
	// intent and outcome so a snapshot that later fails its hash check is
	// enforced as it was last verified (P3-7).
	Enforcement json.RawMessage
	// Snapshot is a policy apply's snapshot as written, recorded on its
	// outcome when the apply succeeded (M3).
	Snapshot string
	// OperationID, when set, is the id the call's records carry: a
	// break-glass record is written before the intent, and links to it.
	OperationID string
	// Plan is the lane's plan function (the Planner): the one plan an
	// approval is asked for, used and shown with. Nil binds an approval to
	// the arguments alone.
	Plan func(context.Context) (plan.Plan, error)
}

// Call is one operation's pair of records (the old build's auditCall).
type Call struct {
	g *Gate
	// telemetry collects what a plugin reports for this call, for the
	// outcome record. Set by WithTelemetry on the plugin paths.
	telemetry *pluginhost.Collector
	// sources collects where each credential the call resolved came from.
	sources *secrets.Sources
	sink    audit.Sink
	logger  *slog.Logger
	start   time.Time
	intent  audit.Record
	spec    Spec
	// target is the resolved target, which egress policy matches on.
	target target.Target
	// egress is what egress policy said about the result, for the outcome.
	egress []audit.EgressAction
	// rates are the rate-limit counts this call holds (P5-b), given back
	// if it is refused.
	rates   []rateHold
	limiter *RateLimiter
	// policyApprove is the policy approve an OwnerAuthorized call deferred to the owner's
	// approval (Call.OwnerApproval); nil when policy did not ask for one.
	policyApprove *policy.Result
}

// Intent is the call's intent record as written, with what the gate added
// since (the approval and plan it runs under).
func (c *Call) Intent() audit.Record {
	if c == nil {
		return audit.Record{}
	}
	return c.intent
}

// Spec is the spec the call was begun with.
func (c *Call) Spec() Spec {
	if c == nil {
		return Spec{}
	}
	return c.spec
}

// Target is the call's resolved target.
func (c *Call) Target() target.Target {
	if c == nil {
		return target.Target{}
	}
	return c.target
}

// pluginPreview reports a dry run a plugin serves itself: nothing is
// meant to change, but the plugin's code runs with its credentials, so the
// brakes, rates and egress treat it as the read it is (M8).
func (spec Spec) pluginPreview() bool {
	return spec.DryRun && spec.Preview == audit.PreviewPluginClaimed
}

// recordRequired reports whether an operation must not run without its
// intent record: every non-read (Decision 8), and anything whose effect is
// unknown.
func (spec Spec) recordRequired() bool {
	return !spec.Known || spec.Op.RequiresAck
}

// beginAudit writes the intent record before anything runs. When the record
// cannot be written, an operation that requires it is refused — the error it
// returns is the refusal to hand the caller — and a read goes on, logged
// loudly (Decision 8), so a full disk does not blind the operator.
func (g *Gate) beginAudit(ctx context.Context, spec Spec) (*Call, error) {
	sink, logger := g.sink, g.logger
	intent := audit.Record{
		Kind:         audit.KindIntent,
		OperationID:  spec.operationIDOrNew(),
		Principal:    principalFor(ctx, spec),
		Reason:       spec.Reason,
		Connector:    spec.Connector,
		Operation:    spec.Operation,
		Effect:       string(spec.Op.Effect),
		ArgsDigest:   sink.Digest(spec.Config),
		Acknowledged: spec.Acknowledged,
		DryRun:       spec.DryRun,

		PluginConfigSHA256:     spec.PluginConfigSHA256,
		PluginEntrypointSHA256: spec.PluginEntrypointSHA256,
		Preview:                spec.Preview,
		PluginReview:           spec.Review,
		Enforcement:            spec.Enforcement,
	}
	var resolved target.Target
	intent.Target, resolved = auditTarget(spec)
	// Shadow mode: every operation a gate covers is authorized and the
	// decision recorded, and nothing about the outcome changes. The monitor
	// is not an operation request and is not authorized (Decision 14).
	// Every record carries the posture it happened under: the one its
	// operation was evaluated with, or, for what nothing authorizes, the
	// applied global posture.
	intent.Posture = g.pdp.GlobalPosture()
	if !spec.Automation {
		intent.Policy, intent.Posture = g.shadowDecision(ctx, spec, resolved)
	}
	call := &Call{g: g, sink: sink, logger: logger, start: time.Now(), intent: intent, spec: spec, target: resolved}
	if _, err := sink.Write(intent); err != nil {
		if spec.recordRequired() && !spec.Automation {
			return nil, fmt.Errorf("the audit log could not be written, so this %s operation was refused and nothing ran; fix the audit directory and retry: %w", effectName(spec), err)
		}
		logger.Error("audit.write_failed", "kind", audit.KindIntent, "connector", spec.Connector, "operation", spec.Operation,
			"effect", spec.Op.Effect, "error", redact.Text(err.Error()), "consequence", "read proceeds unrecorded")
		call.intent.OperationID = "" // nothing to correlate the outcome with
	}
	return call, nil
}

// BeginAudit writes an operation's intent record and nothing more: for what
// is recorded but never gated — Cerberus acting on its own (the monitor, a
// restore), or an administrative act with its own checks (minting a token).
// A non-read whose intent cannot be written is refused; Finish writes the
// outcome.
func (g *Gate) BeginAudit(ctx context.Context, spec Spec) (*Call, error) {
	return g.beginAudit(ctx, spec)
}

// principalFor is who asked, as far as the serving process knows: the
// request's principal (BeginRequest, WithPrincipal), and automation when
// Cerberus acted on its own. A label for the record, never approval.
func principalFor(ctx context.Context, spec Spec) audit.Principal {
	p := audit.Principal{Surface: string(CallerSurfaceFrom(ctx)), SelfReported: true}
	if who, ok := PrincipalFrom(ctx); ok {
		p.Kind, p.Via, p.Client, p.Session, p.OnBehalfOf = string(who.Kind), who.Via, who.Client, who.Session, who.OnBehalfOf
		p.SelfReported = who.SelfReported
		if who.Verified() {
			p.Subject, p.Issuer, p.AuthMethod, p.TokenID = who.Subject, who.Issuer, who.AuthMethod, who.TokenID
			p.Scopes = append([]string(nil), who.Scopes...)
		}
		if who.UID >= 0 { // -1 is unknown
			uid := who.UID
			p.UID, p.UIDVerified = &uid, who.UIDVerified
		}
	}
	if spec.Automation {
		p.Kind = audit.PrincipalAutomation
		p.Surface = string(SurfaceMonitor)
		p.Via = ViaMonitor
		if spec.AutomationVia != "" {
			p.Surface, p.Via = "daemon", spec.AutomationVia
			if spec.AutomationVia == "run_secrets" {
				// Not the daemon: a managed service's own process, before
				// it execs.
				p.Surface = "run_secrets"
			}
		}
		p.SelfReported = false
	}
	return p
}

// AuditPrincipal is the principal ctx's records name: what the gate writes
// on an intent, a decision or a brake change.
func AuditPrincipal(ctx context.Context) audit.Principal { return principalFor(ctx, Spec{}) }

func effectName(spec Spec) string {
	if !spec.Known || spec.Op.Effect == "" {
		return "unclassified"
	}
	return string(spec.Op.Effect)
}

// Note puts a short, Cerberus-composed summary (counts, never values) on the
// outcome record. The intent has already been written, so only the outcome
// carries it.
func (c *Call) Note(text string) {
	if c != nil {
		c.intent.Reason = text
	}
}

// Finish writes the outcome record. The effect has already happened or been
// refused, so a failure here cannot undo anything: it is logged loudly, and
// the missing outcome is visible as an intent with no pair.
func (c *Call) Finish(err error) {
	if c == nil {
		return
	}
	outcome := c.intent
	outcome.Kind = audit.KindOutcome
	outcome.ID = ""
	outcome.DurationMS = time.Since(c.start).Milliseconds()
	outcome.CredentialSources = c.sources.Snapshot()
	code := outcomeCode(err)
	outcome.OutcomeCode = code
	outcome.Decision = audit.DecisionAllowed
	if refusalCodes[code] {
		outcome.Decision = audit.DecisionRefused
		if c.limiter != nil {
			c.limiter.release(c.rates)
		}
	} else if !c.spec.DryRun || len(c.spec.Credentials) > 0 {
		outcome.CredentialNames = c.spec.Credentials
	}
	if t := c.telemetry.Snapshot(); !t.Empty() {
		outcome.PluginTelemetry = auditTelemetry(t)
	}
	outcome.Egress = c.egress
	if code == audit.OutcomeOK && c.spec.Snapshot != "" {
		outcome.PolicySnapshot = c.spec.Snapshot
	}
	if _, werr := c.sink.Write(outcome); werr != nil {
		c.logger.Error("audit.write_failed", "kind", audit.KindOutcome, "operation_id", c.intent.OperationID,
			"connector", c.spec.Connector, "operation", c.spec.Operation, "outcome_code", code, "error", redact.Text(werr.Error()))
	}
	// A real policy denial counts toward the circuit breaker (P5-c).
	if code == string(CodePolicyDenied) && !c.spec.Automation && c.g != nil {
		c.g.noteDenial(c.intent.Principal, c.intent.OperationID, c.g.now())
	}
}

// refusalCodes are the gates' refusals: the operation did not run.
var refusalCodes = map[string]bool{
	string(CodeInvalidArgs):        true,
	string(CodeAckRequired):        true,
	string(CodeUnsupported):        true,
	string(CodePreviewUnsupported): true,
	// Policy's refusals, where enforcement is on.
	string(CodePolicyDenied):     true,
	string(CodeApprovalRequired): true,
	string(CodeApprovalPending):  true,
	string(CodeApprovalExpired):  true,
	string(CodePlanStale):        true,
	// The brakes (§12).
	string(CodeLockdown):          true,
	string(CodeFrozen):            true,
	string(CodeSessionSuspended):  true,
	string(CodeInsufficientScope): true,
	string(CodeAuditUnavailable):  true,
}

func outcomeCode(err error) string {
	if err == nil {
		return audit.OutcomeOK
	}
	var connErr *Error
	if errors.As(err, &connErr) {
		return string(connErr.Code)
	}
	return "error"
}

// auditTarget records the values of the fields the contract says identify
// the target. No other argument value is recorded.
func auditTarget(spec Spec) (audit.Target, target.Target) {
	t := audit.Target{Kind: spec.Op.Target.Kind}
	id := ""
	for _, field := range spec.Op.Target.From {
		value, ok := spec.Config[field]
		if !ok || value == nil {
			continue
		}
		if t.Fields == nil {
			t.Fields = map[string]string{}
		}
		t.Fields[field] = fmt.Sprint(value)
		if id == "" {
			id = t.Fields[field]
		}
	}
	resolved := resolveTarget(spec, id)
	t.Resource, t.Env, t.Owner, t.Admin, t.Adhoc = resolved.Resource, string(resolved.Env), resolved.Owner, resolved.AdminFor, resolved.Adhoc
	t.Tags = resolved.Tags
	return t, resolved
}

// AuditTarget is the target spec's records name, and its resolved labels:
// what a lane's plan function binds as its target.
func AuditTarget(spec Spec) (audit.Target, target.Target) { return auditTarget(spec) }

// resolveTarget labels an operation's target (target.Resolve): from the
// registered resource the call names, by one of its target fields or the
// usual id and resource keys, or unknown. A call that carries a local-only
// input names its target by connection settings, and is ad hoc.
func resolveTarget(spec Spec, id string) target.Target {
	if spec.ResolvedTarget != nil {
		return *spec.ResolvedTarget
	}
	var res *target.ResourceLabels
	if spec.Resources != nil {
		keys := append(append([]string(nil), spec.Op.Target.From...), "id", "resource")
		for _, key := range keys {
			name, _ := spec.Config[key].(string)
			if name == "" {
				continue
			}
			if def, ok := spec.Resources(name); ok && def != nil {
				labels := def.TargetLabels()
				res = &labels
				break
			}
		}
	}
	adhoc := false
	for _, in := range spec.Op.Inputs {
		if in.Scope == contract.InputLocal {
			if _, present := spec.Config[in.Name]; present {
				adhoc = true
			}
		}
	}
	return target.Resolve(spec.Connector, spec.Op.Target.Kind, id, res, adhoc)
}

// WithTelemetry gives a plugin call a collector, so what the plugin reports
// during it lands on this call's outcome record.
func (c *Call) WithTelemetry(ctx context.Context) context.Context {
	if c == nil {
		return ctx
	}
	ctx, c.telemetry = pluginhost.WithTelemetry(ctx)
	return c.WithSources(ctx)
}

// WithSources returns ctx collecting where each credential resolved under it
// came from, for the outcome's credential_sources.
func (c *Call) WithSources(ctx context.Context) context.Context {
	if c == nil {
		return ctx
	}
	ctx, c.sources = secrets.WithSources(ctx)
	return ctx
}

func auditTelemetry(t pluginhost.Telemetry) *audit.PluginTelemetry {
	out := &audit.PluginTelemetry{Stderr: t.Stderr, SharedStderr: t.SharedStderr, Truncated: t.Truncated}
	for _, e := range t.Events {
		out.Events = append(out.Events, audit.PluginEvent{Kind: e.Kind, Message: e.Message, Target: e.Target})
	}
	return out
}

func (spec Spec) operationIDOrNew() string {
	if spec.OperationID != "" {
		return spec.OperationID
	}
	return audit.NewID()
}
