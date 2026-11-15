package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/hostmon"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

// The host and component writes and their history (main's cerbapi host_write_http.go and
// component_http.go), under /api with the old socket's shapes. Reads need cerberus.read and are
// recorded like the other host reads; writes need cerberus.admin and pass the gate as the host
// contract's operation. In observe mode a write answers 409 observe_mode: the old build still owns
// hosts.yaml.

// writeBody is main's: the fields any write takes, unknown fields refused.
type writeBody struct {
	Revision     string                  `json:"revision"`
	Acknowledged bool                    `json:"acknowledged"`
	ApprovalID   string                  `json:"approval_id"`
	Patch        map[string]any          `json:"patch"`
	Create       bool                    `json:"create"`
	DryRun       bool                    `json:"dry_run"`
	Component    map[string]any          `json:"component"`
	To           hosts.ComponentLocation `json:"to"`
}

// args takes the revision from the body, then If-Match, then ?revision (main's).
func (b writeBody) args(r *http.Request) hostmon.WriteArgs {
	rev := b.Revision
	if rev == "" {
		rev = strings.Trim(r.Header.Get("If-Match"), `"`)
	}
	if rev == "" {
		rev = r.URL.Query().Get("revision")
	}
	return hostmon.WriteArgs{Revision: rev, Acknowledged: b.Acknowledged, ApprovalID: b.ApprovalID}
}

func readWriteBody(w http.ResponseWriter, r *http.Request) (writeBody, error) {
	var b writeBody
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return b, fmt.Errorf("%w: the request body could not be read", hostmon.ErrInvalid)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return b, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, fmt.Errorf("%w: the request body is not valid: %w", hostmon.ErrInvalid, err)
	}
	return b, nil
}

func queryLimit(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return n
}

// hostWrite runs one write, or answers that this Cerberus does not write hosts.yaml.
func (a *API) hostWrite(w http.ResponseWriter, r *http.Request, do func(b writeBody, wr *hostmon.Writer) (any, error)) {
	if a.o.Gate == nil || a.o.HostWriter == nil {
		apierr.Write(w, http.StatusConflict, "observe_mode",
			"this Cerberus is in observe mode, so it does not change hosts.yaml: the old Cerberus still owns it", nil)
		return
	}
	b, err := readWriteBody(w, r)
	if err != nil {
		hostErr(w, err)
		return
	}
	ctx := GateContext(r.Context())
	out, err := do(b, a.o.HostWriter)
	if err != nil {
		hostErr(w, err)
		return
	}
	writeScoped(ctx, w, out)
}

func (a *API) hostsRevision(w http.ResponseWriter, r *http.Request) {
	if a.o.HostWriter == nil {
		apierr.Write(w, http.StatusServiceUnavailable, "hosts_unavailable", "this Cerberus serves no host writer", nil)
		return
	}
	rev, err := a.o.HostWriter.Revision()
	if err != nil {
		hostErr(w, err)
		return
	}
	writeScoped(GateContext(r.Context()), w, map[string]string{"revision": rev})
}

func (a *API) hostUpdate(w http.ResponseWriter, r *http.Request) {
	a.hostWrite(w, r, func(b writeBody, wr *hostmon.Writer) (any, error) {
		return wr.UpdateHost(GateContext(r.Context()), hostmon.HostUpdateArgs{WriteArgs: b.args(r), Name: r.PathValue("name"), Patch: b.Patch, Create: b.Create})
	})
}

func (a *API) hostRemove(w http.ResponseWriter, r *http.Request) {
	a.hostWrite(w, r, func(b writeBody, wr *hostmon.Writer) (any, error) {
		return wr.RemoveHost(GateContext(r.Context()), hostmon.HostRemoveArgs{WriteArgs: b.args(r), Name: r.PathValue("name")})
	})
}

func (a *API) hostInventory(w http.ResponseWriter, r *http.Request) {
	a.hostWrite(w, r, func(b writeBody, wr *hostmon.Writer) (any, error) {
		return wr.Inventory(GateContext(r.Context()), hostmon.InventoryArgs{WriteArgs: b.args(r), Name: r.PathValue("name"), DryRun: b.DryRun})
	})
}

func (a *API) componentCreate(w http.ResponseWriter, r *http.Request) {
	a.hostWrite(w, r, func(b writeBody, wr *hostmon.Writer) (any, error) {
		return wr.CreateComponent(GateContext(r.Context()), hostmon.ComponentCreateArgs{WriteArgs: b.args(r), Component: b.Component})
	})
}

func (a *API) componentUpdate(w http.ResponseWriter, r *http.Request) {
	a.hostWrite(w, r, func(b writeBody, wr *hostmon.Writer) (any, error) {
		return wr.UpdateComponent(GateContext(r.Context()), hostmon.ComponentUpdateArgs{WriteArgs: b.args(r), ID: r.PathValue("id"), Patch: b.Patch})
	})
}

func (a *API) componentRemove(w http.ResponseWriter, r *http.Request) {
	a.hostWrite(w, r, func(b writeBody, wr *hostmon.Writer) (any, error) {
		return wr.RemoveComponent(GateContext(r.Context()), hostmon.ComponentRemoveArgs{WriteArgs: b.args(r), ID: r.PathValue("id")})
	})
}

func (a *API) componentMove(w http.ResponseWriter, r *http.Request) {
	a.hostWrite(w, r, func(b writeBody, wr *hostmon.Writer) (any, error) {
		return wr.MoveComponent(GateContext(r.Context()), hostmon.ComponentMoveArgs{WriteArgs: b.args(r), ID: r.PathValue("id"), To: b.To})
	})
}

// history answers a host's or a component's changes, newest first, recorded as a host read.
func (a *API) history(kind, op, param string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, limit := r.PathValue(param), queryLimit(r)
		a.hostRead(w, r, op, map[string]any{"name": name, "limit": limit}, func() (any, error) {
			if a.o.HostWriter == nil {
				return []hosts.Change{}, nil
			}
			return a.o.HostWriter.History(kind, name, limit)
		})
	}
}

func (a *API) componentList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind, host, spare := q.Get("kind"), q.Get("host"), q.Get("spare") == "true"
	a.hostRead(w, r, hostmon.OpComponentList, map[string]any{"filter": map[string]any{"kind": kind, "host": host, "spare": spare}}, func() (any, error) {
		f, err := a.o.Hosts.Declared()
		if err != nil {
			return nil, err
		}
		out := make([]hosts.Component, 0, len(f.Components))
		for _, c := range f.Components {
			if (kind == "" || c.Kind == kind) && (host == "" || c.Location.Host == host) && (!spare || c.Location.Spare) {
				out = append(out, c)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	})
}

func (a *API) componentGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.hostRead(w, r, hostmon.OpComponentGet, map[string]any{"id": id}, func() (any, error) {
		f, err := a.o.Hosts.Declared()
		if err != nil {
			return nil, err
		}
		for _, c := range f.Components {
			if c.ID == id {
				return c, nil
			}
		}
		return nil, fmt.Errorf("%w: component %q is not declared; see `cerberus component list` (components are declared in hosts.yaml)", hostmon.ErrNotFound, id)
	})
}

func (a *API) hostWriteRoutes() []Route {
	return []Route{
		{http.MethodGet, "/api/hosts/_revision", ScopeRead, a.hostsRevision},
		{http.MethodPatch, "/api/hosts/{name}", ScopeAdmin, a.hostUpdate},
		{http.MethodDelete, "/api/hosts/{name}", ScopeAdmin, a.hostRemove},
		{http.MethodPost, "/api/hosts/{name}/inventory", ScopeAdmin, a.hostInventory},
		{http.MethodGet, "/api/hosts/{name}/history", ScopeRead, a.history("host", hostmon.OpHostHistory, "name")},
		{http.MethodGet, "/api/components", ScopeRead, a.componentList},
		{http.MethodGet, "/api/components/_revision", ScopeRead, a.hostsRevision},
		{http.MethodGet, "/api/components/{id}", ScopeRead, a.componentGet},
		{http.MethodGet, "/api/components/{id}/history", ScopeRead, a.history("component", hostmon.OpComponentHistory, "id")},
		{http.MethodPost, "/api/components", ScopeAdmin, a.componentCreate},
		{http.MethodPatch, "/api/components/{id}", ScopeAdmin, a.componentUpdate},
		{http.MethodDelete, "/api/components/{id}", ScopeAdmin, a.componentRemove},
		{http.MethodPost, "/api/components/{id}/move", ScopeAdmin, a.componentMove},
	}
}
