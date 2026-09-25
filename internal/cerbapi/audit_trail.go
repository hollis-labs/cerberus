package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// auditSpec is what an operation's records say about it. It is built from
// the request and the operation's contract, never from its result, so the
// intent can be written before anything runs.
type auditSpec struct {
	connector string
	operation string
	// op is the operation's contract, and known whether one was found. An
	// operation with no contract is treated as needing its record, like any
	// other gap: fail closed.
	op           contract.Operation
	known        bool
	config       map[string]any
	acknowledged bool
	dryRun       bool
	// credentials are the secret names the connector resolves, as
	// "<connector>/<name>". They are recorded on an outcome only when the
	// call got as far as resolving them.
	credentials []string
	// automation marks Cerberus acting on its own. Its record is written but
	// never gates the action: an unwritable log is logged, not a refusal.
	automation bool
	reason     string
	// For a loaded plugin: its config and entrypoint fingerprints.
	pluginConfigSHA256     string
	pluginEntrypointSHA256 string
	// resources resolves a target's registered resource, for its labels.
	resources ResourceLookup
	// preview is plugin_claimed for a dry run a plugin serves itself.
	preview string
	// review is an install review's record.
	review *audit.PluginReview
}

// auditCall is one operation's pair of records.
type auditCall struct {
	// telemetry collects what a plugin reports for this call, for the
	// outcome record. Set by withTelemetry on the plugin paths.
	telemetry *pluginhost.Collector
	sink      audit.Sink
	logger    *slog.Logger
	start     time.Time
	intent    audit.Record
	spec      auditSpec
}

// recordRequired reports whether an operation must not run without its
// intent record: every non-read (Decision 8), and anything whose effect is
// unknown.
func (spec auditSpec) recordRequired() bool {
	return !spec.known || spec.op.RequiresAck
}

// beginAudit writes the intent record before anything runs. When the record
// cannot be written, an operation that requires it is refused — the error it
// returns is the refusal to hand the caller — and a read goes on, logged
// loudly (Decision 8), so a full disk does not blind the operator.
func beginAudit(ctx context.Context, sink audit.Sink, logger *slog.Logger, spec auditSpec) (*auditCall, error) {
	intent := audit.Record{
		Kind:         audit.KindIntent,
		OperationID:  audit.NewID(),
		Principal:    principalFor(ctx, spec),
		Reason:       spec.reason,
		Connector:    spec.connector,
		Operation:    spec.operation,
		Effect:       string(spec.op.Effect),
		ArgsDigest:   sink.Digest(spec.config),
		Acknowledged: spec.acknowledged,
		DryRun:       spec.dryRun,

		PluginConfigSHA256:     spec.pluginConfigSHA256,
		PluginEntrypointSHA256: spec.pluginEntrypointSHA256,
		Preview:                spec.preview,
		PluginReview:           spec.review,
	}
	var resolved target.Target
	intent.Target, resolved = auditTarget(spec)
	// Shadow mode: every operation a gate covers is authorized and the
	// decision recorded, and nothing about the outcome changes. The monitor
	// is not an operation request and is not authorized (Decision 14).
	// Every record carries the posture it happened under: the one its
	// operation was evaluated with, or, for what nothing authorizes, the
	// applied global posture.
	intent.Posture = PolicyDecisionPoint().GlobalPosture()
	if !spec.automation {
		intent.Policy, intent.Posture = shadowDecision(ctx, spec, resolved)
	}
	call := &auditCall{sink: sink, logger: logger, start: time.Now(), intent: intent, spec: spec}
	if _, err := sink.Write(intent); err != nil {
		if spec.recordRequired() && !spec.automation {
			return nil, fmt.Errorf("the audit log could not be written, so this %s operation was refused and nothing ran; fix the audit directory (~/.cerberus/audit) and retry: %w", effectName(spec), err)
		}
		logger.Error("audit.write_failed", "kind", audit.KindIntent, "connector", spec.connector, "operation", spec.operation,
			"effect", spec.op.Effect, "error", redact.Text(err.Error()), "consequence", "read proceeds unrecorded")
		call.intent.OperationID = "" // nothing to correlate the outcome with
	}
	return call, nil
}

// principalFor is who asked, as far as the serving process knows: the
// request's principal (BeginRequest), and automation when Cerberus acted on
// its own. A label for the record, never approval.
func principalFor(ctx context.Context, spec auditSpec) audit.Principal {
	p := audit.Principal{Surface: string(CallerSurfaceFrom(ctx)), SelfReported: true}
	if who, ok := PrincipalFrom(ctx); ok {
		p.Kind, p.Via, p.Client, p.Session, p.OnBehalfOf = string(who.Kind), who.Via, who.Client, who.Session, who.OnBehalfOf
		p.SelfReported = who.SelfReported
		if who.UID >= 0 { // -1 is unknown
			uid := who.UID
			p.UID, p.UIDVerified = &uid, who.UIDVerified
		}
	}
	if spec.automation {
		p.Kind = audit.PrincipalAutomation
		p.Surface = string(SurfaceMonitor)
		p.Via = ViaMonitor
		p.SelfReported = false
	}
	return p
}

func effectName(spec auditSpec) string {
	if !spec.known || spec.op.Effect == "" {
		return "unclassified"
	}
	return string(spec.op.Effect)
}

// finish writes the outcome record. The effect has already happened or been
// refused, so a failure here cannot undo anything: it is logged loudly, and
// the missing outcome is visible as an intent with no pair.
func (c *auditCall) finish(err error) {
	if c == nil {
		return
	}
	outcome := c.intent
	outcome.Kind = audit.KindOutcome
	outcome.ID = ""
	outcome.DurationMS = time.Since(c.start).Milliseconds()
	code := outcomeCode(err)
	outcome.OutcomeCode = code
	outcome.Decision = audit.DecisionAllowed
	if refusalCodes[code] {
		outcome.Decision = audit.DecisionRefused
	} else if !c.spec.dryRun || len(c.spec.credentials) > 0 {
		outcome.CredentialNames = c.spec.credentials
	}
	if t := c.telemetry.Snapshot(); !t.Empty() {
		outcome.PluginTelemetry = auditTelemetry(t)
	}
	if _, werr := c.sink.Write(outcome); werr != nil {
		c.logger.Error("audit.write_failed", "kind", audit.KindOutcome, "operation_id", c.intent.OperationID,
			"connector", c.spec.connector, "operation", c.spec.operation, "outcome_code", code, "error", redact.Text(werr.Error()))
	}
}

// refusalCodes are the gates' refusals: the operation did not run.
var refusalCodes = map[string]bool{
	string(ExternalConnectorInvalidArgs):        true,
	string(ExternalConnectorAckRequired):        true,
	string(ExternalConnectorUnsupported):        true,
	string(ExternalConnectorPreviewUnsupported): true,
	// Policy's refusals, where enforcement is on.
	string(ExternalConnectorPolicyDenied):     true,
	string(ExternalConnectorApprovalRequired): true,
	string(ExternalConnectorApprovalPending):  true,
	string(ExternalConnectorApprovalExpired):  true,
	string(ExternalConnectorPlanStale):        true,
	string(ExternalConnectorAuditUnavailable): true,
}

func outcomeCode(err error) string {
	if err == nil {
		return audit.OutcomeOK
	}
	var connErr *ExternalConnectorError
	if errors.As(err, &connErr) {
		return string(connErr.Code)
	}
	return "error"
}

// auditTarget records the values of the fields the contract says identify
// the target. No other argument value is recorded.
func auditTarget(spec auditSpec) (audit.Target, target.Target) {
	t := audit.Target{Kind: spec.op.Target.Kind}
	id := ""
	for _, field := range spec.op.Target.From {
		value, ok := spec.config[field]
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

// resolveTarget labels an operation's target (target.Resolve): from the
// registered resource the call names, by one of its target fields or the
// usual id and resource keys, or unknown. A call that carries a local-only
// input names its target by connection settings, and is ad hoc.
func resolveTarget(spec auditSpec, id string) target.Target {
	var res *target.ResourceLabels
	if spec.resources != nil {
		keys := append(append([]string(nil), spec.op.Target.From...), "id", "resource")
		for _, key := range keys {
			name, _ := spec.config[key].(string)
			if name == "" {
				continue
			}
			if def, ok := spec.resources(name); ok && def != nil {
				labels := def.TargetLabels()
				res = &labels
				break
			}
		}
	}
	adhoc := false
	for _, in := range spec.op.Inputs {
		if in.Scope == contract.InputLocal {
			if _, present := spec.config[in.Name]; present {
				adhoc = true
			}
		}
	}
	return target.Resolve(spec.connector, spec.op.Target.Kind, id, res, adhoc)
}

// credentialNames are a definition's declared secrets, as the audit record
// names them.
func credentialNames(def contract.Definition) []string {
	var names []string
	for _, secret := range def.Config.Secrets {
		names = append(names, def.ID+"/"+secret.Name)
	}
	sort.Strings(names)
	return names
}

// withTelemetry gives a plugin call a collector, so what the plugin reports
// during it lands on this call's outcome record.
func (c *auditCall) withTelemetry(ctx context.Context) context.Context {
	if c == nil {
		return ctx
	}
	ctx, c.telemetry = pluginhost.WithTelemetry(ctx)
	return ctx
}

func auditTelemetry(t pluginhost.Telemetry) *audit.PluginTelemetry {
	out := &audit.PluginTelemetry{Stderr: t.Stderr, SharedStderr: t.SharedStderr, Truncated: t.Truncated}
	for _, e := range t.Events {
		out.Events = append(out.Events, audit.PluginEvent{Kind: e.Kind, Message: e.Message, Target: e.Target})
	}
	return out
}
