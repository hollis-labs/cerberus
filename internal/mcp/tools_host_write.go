package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// HostWriteTools are the validated writes to hosts.yaml (Hosts C) and the
// change history. Every write needs the revision from cerberus_host_revision
// (or from the previous write's result), the whole resulting file is
// validated before anything is written, and each call is audited.
func HostWriteTools(client cerbapi.HostWriteClient) []Tool {
	obj := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "object", "description": desc}
	}
	rev := map[string]interface{}{"type": "string", "description": "The hosts.yaml revision you read (cerberus_host_revision, or the revision in the last write's result). A write on a stale revision is refused and writes nothing."}
	ack := map[string]interface{}{"type": "boolean", "description": "The operator asked for this change."}
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}
	wargs := func(args map[string]interface{}) cerbapi.WriteArgs {
		return cerbapi.WriteArgs{Revision: stringArg(args, "revision"), Acknowledged: boolArg(args, "acknowledged"), ApprovalID: stringArg(args, "approval_id")}
	}
	object := func(args map[string]interface{}, key string) map[string]any {
		m, _ := args[key].(map[string]interface{})
		return m
	}
	result := func(res cerbapi.WriteResult, err error) (any, error) {
		if err != nil {
			return "", err
		}
		data, merr := redact.MarshalIndent(res, "", "  ")
		return string(data), merr
	}
	history := func(changes []hosts.Change, err error) (any, error) {
		if err != nil {
			return "", err
		}
		data, merr := redact.MarshalIndent(changes, "", "  ")
		return string(data), merr
	}
	limit := map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 500, "description": "Most entries (default 100)."}
	intArg := func(args map[string]interface{}, key string) int {
		if n, ok := args[key].(float64); ok {
			return int(n)
		}
		return 0
	}
	return []Tool{
		contractTool(Tool{
			Name:        "cerberus_host_revision",
			Description: "The current revision of hosts.yaml. Pass it as `revision` on a host or component write.",
			InputSchema: objectSchema(map[string]interface{}{}),
			Handler: func(ctx context.Context, _ map[string]interface{}) (any, error) {
				r, err := client.HostsRevision(ctx)
				if err != nil {
					return "", err
				}
				data, _ := json.Marshal(map[string]string{"revision": r})
				return string(data), nil
			},
		}),
		contractTool(Tool{
			Name: "cerberus_host_update",
			Description: "Change one host in hosts.yaml with a JSON merge patch (labels, notes, role, hardware, ceilings, storage_slots, ...): objects merge, null deletes a field, lists replace. " +
				"The whole resulting file is validated first and a bad change writes nothing. Returns the changed fields (before/after) and the new revision. No credentials in any field.",
			InputSchema: objectSchema(map[string]interface{}{
				"name": str("Host name."), "patch": obj("JSON merge patch of the host record."), "create": map[string]interface{}{"type": "boolean", "description": "Create the host when it does not exist."},
				"revision": rev, "acknowledged": ack,
			}, "name", "patch", "revision"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				return result(client.UpdateHost(ctx, cerbapi.HostUpdateArgs{WriteArgs: wargs(args), Name: stringArg(args, "name"), Patch: object(args, "patch"), Create: boolArg(args, "create")}))
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_host_remove",
			Description: "Remove a host from hosts.yaml. Refused while a component is located on it: move or remove those first.",
			InputSchema: objectSchema(map[string]interface{}{"name": str("Host name."), "revision": rev, "acknowledged": ack}, "name", "revision"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				return result(client.RemoveHost(ctx, cerbapi.HostRemoveArgs{WriteArgs: wargs(args), Name: stringArg(args, "name")}))
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_component_create",
			Description: "Add a spare or installed part (RAM, disk, CPU, other). location is {spare: true} or {host, slot}; a disk or PCIe card in a slot also fills that slot.",
			InputSchema: objectSchema(map[string]interface{}{"component": obj("id, kind (ram|disk|cpu|other), size, type, speed, serial, location, notes."), "revision": rev, "acknowledged": ack}, "component", "revision"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				return result(client.CreateComponent(ctx, cerbapi.ComponentCreateArgs{WriteArgs: wargs(args), Component: object(args, "component")}))
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_component_update",
			Description: "Change a part's fields with a JSON merge patch (size, type, speed, serial, notes ...). Not its location: use cerberus_component_move.",
			InputSchema: objectSchema(map[string]interface{}{"id": str("Component id."), "patch": obj("JSON merge patch."), "revision": rev, "acknowledged": ack}, "id", "patch", "revision"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				return result(client.UpdateComponent(ctx, cerbapi.ComponentUpdateArgs{WriteArgs: wargs(args), ID: stringArg(args, "id"), Patch: object(args, "patch")}))
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_component_move",
			Description: "Move a part to a slot on a host ({host, slot}) or to the spares ({spare: true}). The slot it leaves is freed and the slot it enters is filled; a taken slot is refused.",
			InputSchema: objectSchema(map[string]interface{}{"id": str("Component id."), "to": obj("{host, slot} or {spare: true}."), "revision": rev, "acknowledged": ack}, "id", "to", "revision"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				var to hosts.ComponentLocation
				data, _ := json.Marshal(object(args, "to"))
				if err := json.Unmarshal(data, &to); err != nil {
					return "", fmt.Errorf("to must be {host, slot} or {spare: true}: %w", err)
				}
				return result(client.MoveComponent(ctx, cerbapi.ComponentMoveArgs{WriteArgs: wargs(args), ID: stringArg(args, "id"), To: to}))
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_component_remove",
			Description: "Delete a part from hosts.yaml and free the slot it held.",
			InputSchema: objectSchema(map[string]interface{}{"id": str("Component id."), "revision": rev, "acknowledged": ack}, "id", "revision"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				return result(client.RemoveComponent(ctx, cerbapi.ComponentRemoveArgs{WriteArgs: wargs(args), ID: stringArg(args, "id")}))
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_host_history",
			Description: "The change history of a host, newest first: who changed what, when, with only the changed fields before and after. Includes parts moved on or off the host.",
			InputSchema: objectSchema(map[string]interface{}{"name": str("Host name."), "limit": limit}, "name"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				return history(client.HostHistory(ctx, stringArg(args, "name"), intArg(args, "limit")))
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_component_history",
			Description: "The change history of one component, newest first, with only the changed fields before and after.",
			InputSchema: objectSchema(map[string]interface{}{"id": str("Component id."), "limit": limit}, "id"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				return history(client.ComponentHistory(ctx, stringArg(args, "id"), intArg(args, "limit")))
			},
		}),
	}
}

func init() {
	for _, op := range []string{cerbapi.OpHostRevision, cerbapi.OpHostUpdate, cerbapi.OpHostRemove, cerbapi.OpHostHistory,
		cerbapi.OpComponentCreate, cerbapi.OpComponentUpdate, cerbapi.OpComponentMove, cerbapi.OpComponentRemove, cerbapi.OpComponentHistory} {
		toolOperations["cerberus_"+op] = opRef{cerbapi.HostDefinition, op}
	}
}
