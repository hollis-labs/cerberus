package cerbapi

import (
	"net/http"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/oauth"
)

// RouteSpec describes one route of the host API. HostRoutes is the one list the
// published OpenAPI document is generated from, and a test walks it against the
// real handlers, so a route cannot be documented and missing. The scope shown for
// a route is computed from the operation's contract and oauth.RequiredScopeOp,
// the same function the daemon enforces with, so the documented scope is the
// enforced one.
type RouteSpec struct {
	Method string
	// Path is relative to the mount: /hosts on the daemon socket, /api/hosts and
	// /api/components on the console API.
	Path string
	// Operation is the host connector operation the route runs (a HostDefinition
	// operation name).
	Operation string
	Summary   string
	// Request is a sample value of the request body type; nil for none.
	Request any
	// Response is a sample value of the response type.
	Response any
	// Query lists the documented query parameters.
	Query []string
	// Stream marks a route that also serves text/event-stream.
	Stream bool
	// Static marks a route answered without calling the host service.
	Static bool
	// Alt is a second response type for the same route (rollups for the
	// samples route when grain is 5m, 1h or 1d).
	Alt any
}

// Request bodies. Their JSON field names are the writeBody fields; a test keeps
// the two in step.
type hostUpdateRequest struct {
	Revision     string         `json:"revision"`
	Acknowledged bool           `json:"acknowledged,omitempty"`
	ApprovalID   string         `json:"approval_id,omitempty"`
	Patch        map[string]any `json:"patch"`
	Create       bool           `json:"create,omitempty"`
}

type writeRequest struct {
	Revision     string `json:"revision"`
	Acknowledged bool   `json:"acknowledged,omitempty"`
	ApprovalID   string `json:"approval_id,omitempty"`
}

type componentCreateRequest struct {
	Revision     string          `json:"revision"`
	Acknowledged bool            `json:"acknowledged,omitempty"`
	ApprovalID   string          `json:"approval_id,omitempty"`
	Component    hosts.Component `json:"component"`
}

type componentUpdateRequest struct {
	Revision     string         `json:"revision"`
	Acknowledged bool           `json:"acknowledged,omitempty"`
	ApprovalID   string         `json:"approval_id,omitempty"`
	Patch        map[string]any `json:"patch"`
}

type componentMoveRequest struct {
	Revision     string                  `json:"revision"`
	Acknowledged bool                    `json:"acknowledged,omitempty"`
	ApprovalID   string                  `json:"approval_id,omitempty"`
	To           hosts.ComponentLocation `json:"to"`
}

type inventoryRequest struct {
	Revision     string `json:"revision,omitempty"`
	Acknowledged bool   `json:"acknowledged,omitempty"`
	ApprovalID   string `json:"approval_id,omitempty"`
	DryRun       bool   `json:"dry_run,omitempty"`
}

type revisionResponse struct {
	Revision string `json:"revision"`
}

// HostRoutes is the host API.
func HostRoutes() []RouteSpec {
	return hostRoutes()
}

func hostRoutes() []RouteSpec {
	return []RouteSpec{
		{http.MethodGet, "/hosts", OpHostList, "List declared hosts with recorded state, probed facts, ceilings, storage slots, components and monitor status.", nil, []HostView{}, []string{"role", "provider", "state", "label"}, false, false, nil},
		{http.MethodGet, "/hosts/{name}", OpHostGet, "One host.", nil, HostView{}, nil, false, false, nil},
		{http.MethodPost, "/hosts/{name}/probe", OpHostProbe, "Measure a host now and record the facts.", nil, HostView{}, nil, false, false, nil},
		{http.MethodPost, "/hosts/capacity", OpHostCapacity, "The hosts that satisfy constraints, from recorded facts. A filter: no ranking, no reservation.", capacityQueryWire{}, CapacityResult{}, nil, false, false, nil},
		{http.MethodGet, "/hosts/_revision", OpHostRevision, "The current revision of hosts.yaml: the token every write carries.", nil, revisionResponse{}, nil, false, false, nil},
		{http.MethodPatch, "/hosts/{name}", OpHostUpdate, "Change a host with a JSON merge patch (create adds it). The whole resulting file is validated first.", hostUpdateRequest{}, WriteResult{}, nil, false, false, nil},
		{http.MethodDelete, "/hosts/{name}", OpHostRemove, "Remove a host nothing refers to.", writeRequest{}, WriteResult{}, nil, false, false, nil},
		{http.MethodPost, "/hosts/{name}/inventory", OpHostInventory, "Collect the host's hardware over ssh and record it.", inventoryRequest{}, InventoryResult{}, nil, false, false, nil},
		{http.MethodGet, "/hosts/{name}/history", OpHostHistory, "Changes that touched a host, newest first.", nil, []hosts.Change{}, []string{"limit"}, false, false, nil},
		{http.MethodGet, "/hosts/_status", OpHostStatus, "Health of every host.", nil, []HostStatusView{}, nil, false, false, nil},
		{http.MethodGet, "/hosts/{name}/status", OpHostStatus, "Health of one host: online, degraded, offline or unknown, with reasons.", nil, HostStatusView{}, nil, false, false, nil},
		{http.MethodGet, "/hosts/{name}/samples", OpHostSamples, "A host's raw measurements in time order.", nil, []hosts.Sample{}, []string{"since", "until", "limit"}, false, false, nil},
		{http.MethodGet, "/hosts/_monitor", OpHostMonitor, "What the host monitor is doing: settings and each host's next probe.", nil, MonitorSnapshot{}, nil, false, false, nil},
		{http.MethodGet, "/hosts/_openapi", "", "This document.", nil, map[string]any{}, nil, false, true, nil},
		{http.MethodGet, "/components", OpComponentList, "List spare and installed parts.", nil, []hosts.Component{}, []string{"kind", "host", "spare"}, false, false, nil},
		{http.MethodGet, "/components/{id}", OpComponentGet, "One component.", nil, hosts.Component{}, nil, false, false, nil},
		{http.MethodPost, "/components", OpComponentCreate, "Add a part (a disk or PCIe card in a slot also fills the slot).", componentCreateRequest{}, WriteResult{}, nil, false, false, nil},
		{http.MethodPatch, "/components/{id}", OpComponentUpdate, "Change a part's fields with a merge patch (not its location).", componentUpdateRequest{}, WriteResult{}, nil, false, false, nil},
		{http.MethodPost, "/components/{id}/move", OpComponentMove, "Move a part to a slot on a host, or to the spares.", componentMoveRequest{}, WriteResult{}, nil, false, false, nil},
		{http.MethodDelete, "/components/{id}", OpComponentRemove, "Delete a part and free its slot.", writeRequest{}, WriteResult{}, nil, false, false, nil},
		{http.MethodGet, "/components/{id}/history", OpComponentHistory, "Changes to one component, newest first.", nil, []hosts.Change{}, []string{"limit"}, false, false, nil},
	}
}

// RouteScope is the scope a route needs, as the daemon enforces it.
func RouteScope(r RouteSpec) string {
	if r.Operation == "" {
		return "" // static: no host operation
	}
	op, ok := HostDefinition().Operation(r.Operation)
	if !ok {
		return ""
	}
	return oauth.RequiredScopeOp("host", op.Name, op.Effect)
}

// unpublishedHostOperations are host operations with no route in HostRoutes:
// the Outpost unit actions (their own routes, under operate) and the monitor
// audit record (not a route).
var unpublishedHostOperations = map[string]bool{
	OpMonitorEvent: true,
	"units":        true, "logs": true, "start": true, "stop": true, "restart": true, "plan": true,
}
