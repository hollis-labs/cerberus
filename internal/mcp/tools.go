package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// Owner is the slice whose backend a tool needs, for a tool this build does not serve yet.
const (
	// OwnerHere is a tool served now.
	OwnerHere = ""
	// OwnerSlice9b is the rest of slice 9: the host reads beyond L9 and project_list, which need
	// /api routes this PR does not add yet.
	OwnerSlice9b = "9 (part 2)"
	// OwnerSlice7 is the admin lane: connectors, ssh, docker.
	OwnerSlice7 = "7"
	// OwnerSlice8b is the host writes, components, outpost host actions, capacity, portfolio
	// health, pipelines and ensure_fresh (plan rev 7).
	OwnerSlice8b = "8b"
	// OwnerSchedulerOff is the schedule tools: the scheduling core is ported but off.
	OwnerSchedulerOff = "scheduler-off"
)

// Scopes, as the API names them.
const (
	scopeRead    = api.ScopeRead
	scopeOperate = api.ScopeOperate
	scopeAdmin   = api.ScopeAdmin
)

// handler is a tool's work: args in, an answer out.
type handler func(ctx context.Context, b Backend, args map[string]any) (any, error)

// owners lists the tools not served yet by prefix or name; everything else is served here.
func ownerOf(name string) string {
	switch {
	case strings.HasPrefix(name, "cerberus_schedule_"):
		return OwnerSchedulerOff
	case strings.HasPrefix(name, "cerberus_connector_"), strings.HasPrefix(name, "cerberus_ssh_"), strings.HasPrefix(name, "cerberus_docker_"):
		return OwnerSlice7
	case strings.HasPrefix(name, "cerberus_component_"), strings.HasPrefix(name, "cerberus_pipeline_"):
		return OwnerSlice8b
	}
	switch name {
	case "cerberus_portfolio_health", "cerberus_resource_ensure_fresh", "cerberus_host_capacity",
		"cerberus_host_revision", "cerberus_host_update", "cerberus_host_inventory", "cerberus_host_remove", "cerberus_host_history",
		"cerberus_host_units", "cerberus_host_logs", "cerberus_host_start", "cerberus_host_stop", "cerberus_host_restart", "cerberus_host_plan":
		return OwnerSlice8b
	case "cerberus_project_list", "cerberus_host_get", "cerberus_host_probe", "cerberus_host_status",
		"cerberus_host_samples", "cerberus_host_monitor", "cerberus_host_events":
		return OwnerSlice9b
	}
	return OwnerHere
}

// scopeOf is the scope a tool needs: the scope of the /api route it calls, or for a tool not
// served yet, from its old effect (main's toolOperations): reads read; resource and pipeline
// runs operate; anything that removes or writes configuration, hosts or connectors admin.
func scopeOf(d Def) string {
	switch d.Name {
	case "cerberus_resource_reload", "cerberus_resource_stop", "cerberus_resource_deploy", "cerberus_resource_apply",
		"cerberus_resource_sync", "cerberus_resource_ensure_fresh", "cerberus_pipeline_run":
		return scopeOperate
	case "cerberus_lockdown":
		return scopeAdmin // the /api/brakes/lockdown route's scope
	}
	if d.ReadOnlyHint {
		return scopeRead
	}
	return scopeAdmin
}

// handlers are the tools served now.
var handlers = map[string]handler{
	"cerberus_health":           health,
	"cerberus_resource_list":    resourceList,
	"cerberus_resource_status":  resourceRead("status"),
	"cerberus_resource_inspect": resourceRead("inspect"),
	"cerberus_resource_doctor":  resourceRead("doctor"),
	"cerberus_resource_logs":    resourceLogs,
	"cerberus_resource_reload":  resourceVerb("reload"),
	"cerberus_resource_stop":    resourceVerb("stop"),
	"cerberus_resource_deploy":  resourceVerb("deploy"),
	"cerberus_resource_apply":   resourceVerb("apply"),
	"cerberus_resource_sync":    resourceVerb("sync"),
	"cerberus_resource_remove":  resourceVerb("remove"),
	"cerberus_host_list":        hostList,
	"cerberus_approval_wait":    approvalWait,
	"cerberus_lockdown":         lockdown,
}

func str(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return s
}

func required(field string) (any, error) {
	return toolResult(lifecycleResult{Success: false, Error: field + " is required"})
}

func health(ctx context.Context, b Backend, args map[string]any) (any, error) {
	q := url.Values{}
	if id := str(args, "resource_id"); id != "" {
		q.Set("resource_id", id)
	}
	var h api.DaemonHealth
	if err := b.Do(ctx, http.MethodGet, withQuery("/health", q), nil, &h); err != nil {
		return nil, err
	}
	return indented(h)
}

func resourceList(ctx context.Context, b Backend, args map[string]any) (any, error) {
	q := url.Values{}
	for arg, param := range map[string]string{"project_id": "project_id", "connector": "connector", "tag": "tag"} {
		if v := str(args, arg); v != "" {
			q.Set(param, v)
		}
	}
	var list []api.ResourceInfo
	if err := b.Do(ctx, http.MethodGet, withQuery("/resources", q), nil, &list); err != nil {
		return nil, err
	}
	return budgetedList("cerberus_resource_list", list, args,
		"%d resources match; narrow with project_id, connector, or tag to see the rest."), nil
}

// resourceRead is status, inspect or doctor: the route's answer as indented JSON.
func resourceRead(what string) handler {
	return func(ctx context.Context, b Backend, args map[string]any) (any, error) {
		id := str(args, "resource_id")
		if id == "" {
			return required("resource_id")
		}
		var out json.RawMessage
		if err := b.Do(ctx, http.MethodGet, "/resources/"+url.PathEscape(id)+"/"+what, nil, &out); err != nil {
			return nil, err
		}
		var v any
		if err := json.Unmarshal(out, &v); err != nil {
			return nil, err
		}
		return indented(v)
	}
}

func resourceLogs(ctx context.Context, b Backend, args map[string]any) (any, error) {
	id := str(args, "resource_id")
	if id == "" {
		return required("resource_id")
	}
	lines := 50
	if raw, ok := args["lines"].(float64); ok && raw > 0 {
		lines = int(raw)
	}
	q := url.Values{"lines": {fmt.Sprint(lines)}}
	if s := str(args, "stream"); s != "" {
		q.Set("stream", s)
	}
	if a := str(args, "approval_id"); a != "" {
		q.Set("approval_id", a)
	}
	var out api.LogLines
	if err := b.Do(ctx, http.MethodGet, withQuery("/resources/"+url.PathEscape(id)+"/logs", q), nil, &out); err != nil {
		return nil, err
	}
	return indented(out)
}

// resourceVerb is one of the six mutations, through L7/L8 and so through the gate.
func resourceVerb(verb string) handler {
	return func(ctx context.Context, b Backend, args map[string]any) (any, error) {
		id := str(args, "resource_id")
		if id == "" {
			return required("resource_id")
		}
		ack, _ := args["acknowledged"].(bool)
		body := map[string]any{"acknowledged": ack}
		if a := str(args, "approval_id"); a != "" {
			body["approval_id"] = a
		}
		var res api.OpResult
		if err := b.Do(ctx, http.MethodPost, "/resources/"+url.PathEscape(id)+"/"+verb, body, &res); err != nil {
			return nil, err
		}
		return toolResult(lifecycleResult{Success: res.Success, Message: res.Message, Error: res.Error})
	}
}

func hostList(ctx context.Context, b Backend, args map[string]any) (any, error) {
	q := url.Values{}
	for _, k := range []string{"role", "provider", "state"} {
		if v := str(args, k); v != "" {
			q.Set(k, v)
		}
	}
	if labels, ok := args["labels"].(map[string]any); ok {
		for k, v := range labels {
			if s, ok := v.(string); ok {
				q.Add("label", k+"="+s)
			}
		}
	}
	var views []api.HostView
	if err := b.Do(ctx, http.MethodGet, withQuery("/hosts", q), nil, &views); err != nil {
		return nil, err
	}
	var notes []string
	for _, v := range views {
		if note := hostNote(v); note != "" {
			notes = append(notes, note)
		}
	}
	var envelope any
	if err := json.Unmarshal([]byte(budgetedList("cerberus_host_list", views, args, "%d hosts total.")), &envelope); err != nil {
		return nil, err
	}
	return withNotes(envelope, notes)
}

// hostNote, probedAt and withNotes are main's (tools_host.go): one line per host whose facts
// are unknown, offline or stale, appended to the answer as notes.
func hostNote(v api.HostView) string {
	name := v.Host.Name
	switch {
	case v.Probed == nil && v.State == "offline":
		return fmt.Sprintf("%s: offline and never measured (%s). Its facts are unknown.", name, v.Reason)
	case v.Probed == nil:
		return fmt.Sprintf("%s: never probed, so its facts are unknown. Call cerberus_host_probe to measure it.", name)
	case v.State == "offline":
		return fmt.Sprintf("%s: offline (%s). The facts shown are the last good measurement%s.", name, v.Reason, probedAt(v))
	case v.Stale:
		return fmt.Sprintf("%s: the facts are stale%s. Call cerberus_host_probe for current ones.", name, probedAt(v))
	}
	return ""
}

func probedAt(v api.HostView) string {
	if v.ProbedAt == nil {
		return ""
	}
	return ", measured " + v.ProbedAt.UTC().Format("2006-01-02T15:04:05Z")
}

func withNotes(body any, notes []string) (string, error) {
	data, err := redact.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", err
	}
	if len(notes) == 0 {
		return string(data), nil
	}
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err = dec.Decode(&obj); err != nil {
		return "", err
	}
	obj["notes"] = notes
	out, err := redact.MarshalIndent(obj, "", "  ")
	return string(out), err
}

// The approval wait polls the approval until its status changes or the timeout (main's).
const (
	approvalWaitDefault = 30 * time.Second
	approvalWaitMax     = 60 * time.Second
)

// approvalPollInterval is how often the wait reads the approval; tests shorten it.
var approvalPollInterval = time.Second

type approvalWaitResult struct {
	Approval json.RawMessage `json:"approval"`
	Changed  bool            `json:"changed"`
	Waited   string          `json:"waited"`
	NextStep string          `json:"next_step"`
}

func approvalWait(ctx context.Context, b Backend, args map[string]any) (any, error) {
	id := str(args, "id")
	if id == "" {
		return toolResult(lifecycleResult{Success: false, Error: "id is required: the approval id from approval_pending"})
	}
	timeout := approvalWaitDefault
	if secs, ok := args["timeout_seconds"].(float64); ok && secs > 0 {
		timeout = min(time.Duration(secs*float64(time.Second)), approvalWaitMax)
	}
	read := func() (json.RawMessage, string, error) {
		var raw json.RawMessage
		if err := b.Do(ctx, http.MethodGet, "/approvals/"+url.PathEscape(id), nil, &raw); err != nil {
			return nil, "", err
		}
		var head struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &head)
		return raw, head.Status, nil
	}
	start := time.Now()
	raw, first, err := read()
	if err != nil {
		return nil, err
	}
	status := first
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(approvalPollInterval)
	defer tick.Stop()
wait:
	for status == first && status == "pending" {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			break wait
		case <-tick.C:
		}
		if raw, status, err = read(); err != nil {
			return nil, err
		}
	}
	return approvalWaitResult{Approval: raw, Changed: status != first, Waited: time.Since(start).Round(time.Second).String(),
		NextStep: waitNextStep(id, status).Error()}, nil
}

func waitNextStep(id, status string) error {
	switch status {
	case "pending":
		return redact.Guidance("still pending; call cerberus_approval_wait again with id %s to keep waiting, or remind your operator to decide it on the Cerberus console", id)
	case "approved":
		return redact.Guidance("approved: retry the call that asked for it, with the same arguments and approval_id %s. It can be used once", id)
	case "denied":
		return redact.Guidance("denied by your operator: do not retry; ask them what they would like instead")
	case "expired":
		return redact.Guidance("expired unused: retry the call without approval_id to ask again")
	case "consumed":
		return redact.Guidance("already used by a call; a new call needs a new approval")
	case "revoked":
		return redact.Guidance("revoked by your operator before it was used: do not retry; ask them why")
	}
	return fmt.Errorf("approval %s is %s", id, status)
}

type lockdownResult struct {
	Success  bool            `json:"success"`
	Brakes   json.RawMessage `json:"brakes"`
	NextStep string          `json:"next_step"`
}

func lockdown(ctx context.Context, b Backend, args map[string]any) (any, error) {
	var out struct {
		State json.RawMessage `json:"state"`
	}
	if err := b.Do(ctx, http.MethodPost, "/brakes/lockdown", map[string]any{"reason": str(args, "reason")}, &out); err != nil {
		return nil, err
	}
	return lockdownResult{Success: true, Brakes: out.State,
		NextStep: "Cerberus is in lockdown: only plain reads run. Tell your operator why; the owner lifts it on the Cerberus console."}, nil
}

func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// notYet answers a tool whose backend this build does not have.
func notYet(name, owner string) handler {
	return func(context.Context, Backend, map[string]any) (any, error) {
		if owner == OwnerSchedulerOff {
			return toolResult(lifecycleResult{Success: false, Error: name + ": the scheduler is off in this Cerberus (cerberus.scheduler.enabled)"})
		}
		return toolResult(lifecycleResult{Success: false, Error: fmt.Sprintf("%s is not available in this build (slice %s of the port)", name, owner)})
	}
}
