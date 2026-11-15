package mcp

import (
	"context"
	"net/http"
	"net/url"

	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The admin lane's tools (slice 7): main's tools_connectors.go, tools_ssh.go and tools_docker.go
// over GET /api/connectors and POST /api/connectors/{id}/operations/{op}, which the gate guards.

func init() {
	for name, h := range map[string]handler{
		"cerberus_connector_list":     connectorList,
		"cerberus_connector_describe": connectorDescribe,
		"cerberus_ssh_exec":           sshExec,
		"cerberus_ssh_status":         sshStatus,
		"cerberus_ssh_put":            sshTransfer("put", true),
		"cerberus_ssh_get":            sshTransfer("get", false),
		"cerberus_ssh_put_dir":        sshTransfer("put_dir", true),
		"cerberus_ssh_get_dir":        sshTransfer("get_dir", false),
		"cerberus_docker_ps":          dockerPS,
		"cerberus_docker_logs":        dockerLogs,
		"cerberus_docker_up":          dockerLifecycle("start", "started"),
		"cerberus_docker_down":        dockerLifecycle("stop", "stopped"),
		"cerberus_docker_destroy":     dockerLifecycle("destroy", "destroyed"),
	} {
		handlers[name] = h
	}
}

// opResult is the operations route's answer.
type opResult struct {
	Connector string `json:"connector"`
	Operation string `json:"operation"`
	Data      any    `json:"data"`
}

// operation runs one admin-lane operation.
func operation(ctx context.Context, b Backend, connector, op string, cfg map[string]any, dryRun, ack bool, args map[string]any) (opResult, error) {
	body := map[string]any{"config": cfg}
	if dryRun {
		body["dry_run"] = true
	}
	if ack {
		body["acknowledged"] = true
	}
	if a := str(args, "approval_id"); a != "" {
		body["approval_id"] = a
	}
	var out opResult
	err := b.Do(ctx, http.MethodPost, "/connectors/"+url.PathEscape(connector)+"/operations/"+url.PathEscape(op), body, &out)
	return out, err
}

// connectorData is main's marshalConnectorData: text as redacted text, nothing as success,
// anything else as indented redacted JSON.
func connectorData(data any) (any, error) {
	if text, ok := data.(string); ok {
		return redact.Text(text), nil
	}
	if data == nil {
		return indented(lifecycleResult{Success: true})
	}
	return indented(data)
}

func boolArg(args map[string]any, k string) bool {
	v, _ := args[k].(bool)
	return v
}

func connectorList(ctx context.Context, b Backend, args map[string]any) (any, error) {
	var defs []contract.Definition
	if err := b.Do(ctx, http.MethodGet, "/connectors", nil, &defs); err != nil {
		return nil, err
	}
	return budgetedList("cerberus_connector_list", defs, args, "%d connectors total."), nil
}

func connectorDescribe(ctx context.Context, b Backend, args map[string]any) (any, error) {
	id := str(args, "id")
	if id == "" {
		return toolResult(lifecycleResult{Success: false, Error: `missing "id"`})
	}
	var defs []contract.Definition
	if err := b.Do(ctx, http.MethodGet, "/connectors", nil, &defs); err != nil {
		return nil, err
	}
	for _, def := range defs {
		if def.ID == id {
			data, err := redact.MarshalIndent(redact.DeclaredSchema{Value: def}, "", "  ")
			if err != nil {
				return nil, err
			}
			return string(data), nil
		}
	}
	return toolResult(lifecycleResult{Success: false, Error: "connector not found: " + id})
}

func sshConfig(resourceID, command string) map[string]any {
	cfg := map[string]any{"id": resourceID}
	if command != "" {
		cfg["command"] = command
	}
	return cfg
}

func sshExec(ctx context.Context, b Backend, args map[string]any) (any, error) {
	out, err := operation(ctx, b, "ssh", "exec", sshConfig(str(args, "resource_id"), str(args, "command")),
		boolArg(args, "dry_run"), boolArg(args, "acknowledged"), args)
	if err != nil {
		return nil, err
	}
	return connectorData(out.Data)
}

func sshStatus(ctx context.Context, b Backend, args map[string]any) (any, error) {
	out, err := operation(ctx, b, "ssh", "status", sshConfig(str(args, "resource_id"), ""), false, false, map[string]any{})
	if err != nil {
		return nil, err
	}
	return connectorData(out.Data)
}

// sshTransfer is main's runSSHTransfer; uploads take dry_run, downloads do not.
func sshTransfer(op string, takesDryRun bool) handler {
	return func(ctx context.Context, b Backend, args map[string]any) (any, error) {
		cfg := sshConfig(str(args, "resource_id"), "")
		cfg["local_path"], cfg["remote_path"] = str(args, "local_path"), str(args, "remote_path")
		out, err := operation(ctx, b, "ssh", op, cfg, takesDryRun && boolArg(args, "dry_run"), boolArg(args, "acknowledged"), args)
		if err != nil {
			return nil, err
		}
		return connectorData(out.Data)
	}
}

func dockerPS(ctx context.Context, b Backend, _ map[string]any) (any, error) {
	out, err := operation(ctx, b, "docker", "list_containers", nil, false, false, map[string]any{})
	if err != nil {
		return nil, err
	}
	return connectorData(out.Data)
}

// dockerLogsResult is main's (egress_marker.go).
type dockerLogsResult struct {
	Container string `json:"container"`
	Lines     int    `json:"lines"`
	Output    string `json:"output" cerb:"untrusted"`
}

func dockerLogs(ctx context.Context, b Backend, args map[string]any) (any, error) {
	container := str(args, "container")
	lines := 50
	if l, ok := args["lines"].(float64); ok && l > 0 {
		lines = int(l)
	}
	out, err := operation(ctx, b, "docker", "logs", map[string]any{"container": container, "lines": lines}, false, false, args)
	if err != nil {
		return nil, err
	}
	logs, _ := out.Data.(string)
	return connectorData(dockerLogsResult{Container: container, Lines: lines, Output: logs})
}

// dockerLifecycle is main's newDockerLifecycleTool.
func dockerLifecycle(op, verb string) handler {
	return func(ctx context.Context, b Backend, args map[string]any) (any, error) {
		resourceID, container := str(args, "resource_id"), str(args, "container_name")
		var cfg map[string]any
		switch {
		case resourceID != "" && container != "":
			return toolResult(lifecycleResult{Success: false, Error: "pass container_name or resource_id, not both"})
		case resourceID != "":
			cfg = map[string]any{"resource": resourceID}
		case container != "":
			cfg = map[string]any{"container": container, "id": container, "name": container}
		default:
			return toolResult(lifecycleResult{Success: false, Error: "one of container_name or resource_id is required"})
		}
		if _, err := operation(ctx, b, "docker", op, cfg, false, boolArg(args, "acknowledged"), args); err != nil {
			return nil, err
		}
		if resourceID != "" {
			return toolResult(lifecycleResult{Success: true, ServiceID: resourceID, Message: "resource " + verb})
		}
		return toolResult(lifecycleResult{Success: true, ServiceID: container, Message: "container " + verb})
	}
}
