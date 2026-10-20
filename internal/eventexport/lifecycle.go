package eventexport

import (
	"fmt"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
)

const (
	schemaResourceState = "urn:hollis-labs:events:resource.state_changed@1"
	schemaDeployState   = "urn:hollis-labs:events:deploy.state_changed@1"
)

// Lifecycle states (the catalog's resource.state_changed vocabulary) and deploy
// states (deploy.state_changed).
const (
	stateStopped   = "stopped"
	stateRunning   = "running"
	stateDeploying = "deploying"
	stateFailed    = "failed"
	stateRemoved   = "removed"

	deployStarted   = "started"
	deploySucceeded = "succeeded"
	deployFailed    = "failed"
)

// MapLifecycle maps the outcome record of an operation that changed a resource's
// lifecycle to resource.state_changed (and, for a deploy, deploy.state_changed),
// or returns nil. It reads only the audit chain, which already records every
// resource mutation made through the supervision lane, whoever made it (the CLI,
// MCP, the console, the scheduler, the monitor's restarts).
//
// Only an outcome that ran is mapped. An intent says nothing yet, a refused
// outcome did not run, a dry run changes nothing, and sync and the read
// operations change no lifecycle state. What each operation means:
//
//   - stop, ok: to stopped (reason operator_stop when a person or agent asked);
//   - apply, ok: to running; reload, ok: to running, reason restart;
//   - remove, ok: to removed;
//   - deploy: deploy.state_changed started then succeeded or failed; on success
//     the resource also goes deploying then running (reason deploy). All come
//     from the one outcome record, so started is announced after the fact with
//     its true time (the outcome's time minus its duration) and the operation id
//     as correlation id;
//   - a failed operation (apply, reload, deploy, stop, remove) says only that the
//     operation failed, not what state the resource is in (a failed build leaves
//     the previous process running), so no resource state is exported for it; a
//     failed deploy still exports deploy.state_changed failed.
//
// What this does not see is anything that did not go through an audited
// operation: a process that crashes, a health check that fails, a host that
// reboots. That detection is a separate piece of work and is not guessed here.
// The previous state (from) is not known to the audit chain and is left out.
func MapLifecycle(rec audit.Record) []Mapped {
	if rec.Kind != audit.KindOutcome || rec.Connector != "local" || rec.DryRun || rec.Decision != audit.DecisionAllowed {
		return nil
	}
	id := rec.Target.Fields["id"]
	if id == "" {
		id = rec.Target.Resource
	}
	if id == "" || rec.OperationID == "" {
		return nil
	}
	ok := rec.OutcomeCode == audit.OutcomeOK
	trigger := triggerOf(rec.Principal)
	labels := lifecycleLabels(rec)
	key := fmt.Sprintf("a/%d/%s/life", rec.Seq, hashTag(rec.Hash))
	end := rec.Time.UTC()
	start := end.Add(-time.Duration(rec.DurationMS) * time.Millisecond)

	var out []Mapped
	add := func(env Envelope) { out = append(out, Mapped{Key: fmt.Sprintf("%s/%d", key, len(out)), Envelope: env}) }
	state := func(at time.Time, from, to, reason string) {
		add(resourceState(at, id, rec, from, to, reason, trigger, labels))
	}

	switch rec.Operation {
	case "stop":
		if ok {
			reason := "operator_stop"
			if rec.Principal.Kind == audit.PrincipalAutomation {
				reason = ""
			}
			state(end, "", stateStopped, reason)
		}
	case "apply":
		if ok {
			state(end, "", stateRunning, "")
		}
	case "reload":
		if ok {
			state(end, "", stateRunning, "restart")
		}
	case "remove":
		if ok {
			state(end, "", stateRemoved, "")
		}
	case "deploy":
		add(deployState(start, id, rec, "", deployStarted, "", trigger))
		if ok {
			state(start, "", stateDeploying, "deploy")
			add(deployState(end, id, rec, deployStarted, deploySucceeded, "", trigger))
			state(end, stateDeploying, stateRunning, "deploy")
		} else {
			// A failed deploy says nothing about the resource: a failed build
			// leaves the previous process running, so no resource state is claimed.
			add(deployState(end, id, rec, deployStarted, deployFailed, "", trigger))
		}
	}
	return out
}

// triggerOf is what asked for the change: a person, an agent, the scheduler, or
// Cerberus itself.
func triggerOf(p audit.Principal) map[string]any {
	kind := "system"
	switch {
	case p.Surface == "scheduler":
		kind = "scheduler"
	case p.Kind == "human" || p.Kind == "agent":
		kind = p.Kind
	}
	return map[string]any{"kind": kind}
}

// lifecycleLabels are the policy labels the audit record carries for the
// resource. Strings only; no configuration values, paths or environment.
func lifecycleLabels(rec audit.Record) map[string]string {
	l := map[string]string{"connector": rec.Connector}
	setIfStr(l, "env", rec.Target.Env)
	setIfStr(l, "owner", rec.Target.Owner)
	setIfStr(l, "admin", rec.Target.Admin)
	return l
}

func setIfStr(m map[string]string, key, value string) {
	if value != "" {
		m[key] = value
	}
}

func resourceState(at time.Time, id string, rec audit.Record, from, to, reason string, trigger map[string]any, labels map[string]string) Envelope {
	p := map[string]any{
		"entity":  map[string]string{"type": "resource", "id": id},
		"to":      to,
		"trigger": trigger,
		"labels":  labels,
	}
	setIf(p, "from", from)
	setIf(p, "reason", reason)
	severity := "info"
	if to == stateFailed {
		severity = "warning"
	}
	return Envelope{
		Kind:    runtimeevents.EventKind("resource.state_changed"),
		Time:    at.UTC(),
		Subject: "msg://resource/cerberus/" + id,
		Source:  Source{Channel: ChannelAudit},
		Corr:    corr(rec.OperationID),
		Actor:   actorOf(rec.Principal),
		Class:   &Class{Category: "state", Severity: severity, Visibility: VisibilityEnvironment, PayloadSchema: schemaResourceState},
		Payload: mustJSON(p),
	}
}

func deployState(at time.Time, id string, rec audit.Record, from, to, reason string, trigger map[string]any) Envelope {
	p := map[string]any{
		"entity":   map[string]string{"type": "deployment", "id": rec.OperationID},
		"resource": map[string]string{"id": id},
		"to":       to,
		"trigger":  trigger,
	}
	setIf(p, "from", from)
	setIf(p, "reason", reason)
	severity := "info"
	if to == deployFailed {
		severity = "warning"
	}
	return Envelope{
		Kind:    runtimeevents.EventKind("deploy.state_changed"),
		Time:    at.UTC(),
		Subject: "msg://deployment/cerberus/" + rec.OperationID,
		Source:  Source{Channel: ChannelAudit},
		Corr:    corr(rec.OperationID),
		Actor:   actorOf(rec.Principal),
		Class:   &Class{Category: "state", Severity: severity, Visibility: VisibilityEnvironment, PayloadSchema: schemaDeployState},
		Payload: mustJSON(p),
	}
}
