package hostmon

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The host operations' contract, main's cerbapi.HostDefinition moved unchanged: the gate records
// each host read and write as one of these operations (EffectRead for the reads, a probe
// included: its commands are fixed).

// Host operation names, shared by the MCP tools, the API and the contract.
const (
	OpHostList         = "list"
	OpHostGet          = "get"
	OpHostProbe        = "probe"
	OpHostCapacity     = "capacity"
	OpComponentList    = "component_list"
	OpComponentGet     = "component_get"
	OpHostStatus       = "host_status"
	OpHostSamples      = "host_samples"
	OpHostMonitor      = "host_monitor"
	OpMonitorEvent     = "monitor_event"
	OpHostEvents       = "host_events"
	OpHostUpdate       = "host_update"
	OpHostRemove       = "host_remove"
	OpComponentCreate  = "component_create"
	OpComponentUpdate  = "component_update"
	OpComponentMove    = "component_move"
	OpComponentRemove  = "component_remove"
	OpHostRevision     = "host_revision"
	OpHostHistory      = "host_history"
	OpHostInventory    = "host_inventory"
	OpComponentHistory = "component_history"
)

func fromFields(from string) []string {
	if from == "" {
		return nil
	}
	return []string{from}
}

// HostDefinition is the contract of the host operations. All four are reads:
// a probe measures, it never changes the host.
func HostDefinition() contract.Definition {
	read := func(name, desc string, from []string, inputs ...contract.Input) contract.Operation {
		return contract.Operation{
			Name: name, Description: desc, Effect: contract.EffectRead,
			Target:  contract.TargetDescriptor{Kind: "host", From: from},
			Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone,
			Inputs: inputs,
		}
	}
	write := func(name, kind, from, desc string, inputs ...contract.Input) contract.Operation {
		return contract.Operation{
			Name: name, Description: desc, Effect: contract.EffectWrite, Reversible: true,
			Target:  contract.TargetDescriptor{Kind: kind, From: fromFields(from)},
			Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSWrites,
			Inputs: inputs,
		}.Finalize()
	}
	labels := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Labels that must all match exactly."}
	size := func(desc string) map[string]any {
		return map[string]any{"type": []string{"string", "integer"}, "description": desc}
	}
	name := contract.RequiredField("name", contract.StringSchema("Host name."))
	return contract.Finalize(contract.Definition{ID: "host", Version: "builtin", ResourceTypes: []string{"host"}, Operations: []contract.Operation{
		read(OpHostList, "List declared hosts with their recorded state and probed facts.", nil,
			contract.Field("role", contract.StringSchema("Only hosts with this role.")),
			contract.Field("provider", contract.StringSchema("Only hosts with this provider.")),
			contract.Field("state", contract.StringSchema("online, offline or unknown.")),
			contract.Field("labels", labels)),
		read(OpHostGet, "Show one host.", []string{"name"}, name),
		read(OpHostProbe, "Measure one host over ssh and record the facts. A read: the commands are fixed.", []string{"name"}, name),
		read(OpComponentList, "List spare and installed parts (components) recorded in hosts.yaml.", nil,
			contract.Field("kind", contract.StringSchema("ram, disk, cpu or other.")),
			contract.Field("host", contract.StringSchema("Only components located on this host.")),
			contract.Field("spare", map[string]any{"type": "boolean", "description": "Only spare components."})),
		read(OpComponentGet, "Show one component.", nil, contract.RequiredField("id", contract.StringSchema("Component id."))),
		write(OpHostUpdate, "host", "name", "Change one host with a merge patch (labels, notes, role, hardware, ceilings, storage_slots, ...). Validated as a whole file; needs the revision you read.",
			contract.RequiredField("name", contract.StringSchema("Host name.")),
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read.")),
			contract.Field("patch", map[string]any{"type": "object", "description": "JSON merge patch: objects merge, null deletes, lists replace."}),
			contract.Field("create", map[string]any{"type": "boolean", "description": "Create the host when it does not exist."})),
		write(OpHostRemove, "host", "name", "Remove a host no component refers to.",
			contract.RequiredField("name", contract.StringSchema("Host name.")),
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read."))),
		write(OpComponentCreate, "component", "", "Add a spare or installed part.",
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read.")),
			contract.Field("component", map[string]any{"type": "object", "description": "id, kind, size, type, speed, serial, location, notes."})),
		write(OpComponentUpdate, "component", "id", "Change a part's fields (not its location).",
			contract.RequiredField("id", contract.StringSchema("Component id.")),
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read.")),
			contract.Field("patch", map[string]any{"type": "object", "description": "JSON merge patch."})),
		write(OpComponentMove, "component", "id", "Move a part to a slot on a host, or to the spares.",
			contract.RequiredField("id", contract.StringSchema("Component id.")),
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read.")),
			contract.Field("to", map[string]any{"type": "object", "description": "{host, slot} or {spare: true}."})),
		write(OpComponentRemove, "component", "id", "Delete a part.",
			contract.RequiredField("id", contract.StringSchema("Component id.")),
			contract.RequiredField("revision", contract.StringSchema("The hosts.yaml revision you read."))),
		read(OpHostHistory, "Changes that touched a host, newest first.", []string{"name"}, name,
			contract.Field("limit", contract.IntegerSchema("Most entries to return."))),
		read(OpComponentHistory, "Changes to one component, newest first.", []string{"id"},
			contract.RequiredField("id", contract.StringSchema("Component id.")),
			contract.Field("limit", contract.IntegerSchema("Most entries to return."))),
		write(OpHostInventory, "host", "name", "Collect the host's physical facts over ssh (sudo -n for root-only ones) and record them; operator-set notes, storage_slots, vendor-sourced ceilings and components are kept. revision is optional.",
			contract.RequiredField("name", contract.StringSchema("Host name.")),
			contract.Field("revision", contract.StringSchema("Optional: the hosts.yaml revision you read.")),
			contract.Field("dry_run", map[string]any{"type": "boolean", "description": "Collect and show what would change; write nothing."})),
		read(OpHostStatus, "Health of one host (online, degraded, offline, unknown) with reasons, or of every host when no name is given.", nil,
			contract.Field("name", contract.StringSchema("Host name."))),
		read(OpHostSamples, "A host's retained measurements in time order.", []string{"name"},
			contract.RequiredField("name", contract.StringSchema("Host name.")),
			contract.Field("since", contract.StringSchema("RFC 3339 time or a duration such as 6h.")),
			contract.Field("until", contract.StringSchema("RFC 3339 time.")),
			contract.Field("limit", contract.IntegerSchema("Most samples (default 500)."))),
		read(OpHostEvents, "Status-change events after an id, oldest first, with the id to resume from.", nil,
			contract.Field("after_id", contract.IntegerSchema("Resume after this event id.")),
			contract.Field("host", contract.StringSchema("Only this host.")),
			contract.Field("kind", contract.StringSchema("Only this kind.")),
			contract.Field("limit", contract.IntegerSchema("Most events (default 200)."))),
		read(OpHostMonitor, "What the host monitor is doing: settings and each host's interval and next probe.", nil),
		read(OpMonitorEvent, "A record of the host monitor acting on its own: a status change, a failed-probe streak, an hourly summary.", nil),
		read(OpHostRevision, "The current revision of hosts.yaml: the token every write carries.", nil),
		read(OpHostCapacity, "List the hosts that satisfy constraints, from recorded facts. Facts only: no ranking.", nil,
			contract.Field("min_memory_available", size("Bytes or a size such as 4GiB.")),
			contract.Field("min_disk_free", size("Bytes or a size such as 50GB.")),
			contract.Field("min_cpu", contract.IntegerSchema("Minimum probed logical cores.")),
			contract.Field("role", contract.StringSchema("Declared role.")),
			contract.Field("provider", contract.StringSchema("Provider.")),
			contract.Field("labels", labels),
			contract.Field("require_tools", map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Probed tools that must be installed."}),
			contract.Field("max_stale_age", contract.StringSchema("Oldest probe a measured constraint is judged on, such as 10m.")),
			contract.Field("include_offline", map[string]any{"type": "boolean", "description": "Judge offline hosts on their last measurement."}),
			contract.Field("explain", map[string]any{"type": "boolean", "description": "Also list excluded hosts with reasons."}),
			contract.Field("min_memory_total", size("Installed memory at least this.")),
			contract.Field("min_memory_upgrade", size("Room to add at least this much memory.")),
			contract.Field("free_memory_slots", contract.IntegerSchema("At least this many empty memory slots.")),
			contract.Field("free_storage_slots", map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "Free storage slots required by kind: [{kind, min}]."}),
			contract.Field("disk_types", map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "At least one recorded disk of these types."}),
			contract.Field("min_disk_size", size("At least one recorded disk at least this large.")),
			contract.Field("status_in", map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Monitor status must be one of these."}),
			contract.Field("recent", map[string]any{"type": "object", "description": "Headroom held over a window: window, max_load_per_core, min_memory_available, min_disk_free."})),
	}})
}
