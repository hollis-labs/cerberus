package cerbapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// HostWriteClient is the write and history side of the host service (Hosts C),
// separate from HostClient so older fakes keep compiling.
type HostWriteClient interface {
	HostsRevision(ctx context.Context) (string, error)
	UpdateHost(ctx context.Context, a HostUpdateArgs) (WriteResult, error)
	RemoveHost(ctx context.Context, a HostRemoveArgs) (WriteResult, error)
	CreateComponent(ctx context.Context, a ComponentCreateArgs) (WriteResult, error)
	UpdateComponent(ctx context.Context, a ComponentUpdateArgs) (WriteResult, error)
	MoveComponent(ctx context.Context, a ComponentMoveArgs) (WriteResult, error)
	RemoveComponent(ctx context.Context, a ComponentRemoveArgs) (WriteResult, error)
	HostHistory(ctx context.Context, name string, limit int) ([]hosts.Change, error)
	ComponentHistory(ctx context.Context, id string, limit int) ([]hosts.Change, error)
}

// HostWriteClientOf returns client as a HostWriteClient, or one that refuses
// every call with the reason.
func HostWriteClientOf(client any) HostWriteClient {
	if w, ok := client.(HostWriteClient); ok {
		return w
	}
	return unavailableHostClient{}
}

func (unavailableHostClient) HostsRevision(context.Context) (string, error) {
	return "", errHostsUnavailable
}
func (unavailableHostClient) UpdateHost(context.Context, HostUpdateArgs) (WriteResult, error) {
	return WriteResult{}, errHostsUnavailable
}
func (unavailableHostClient) RemoveHost(context.Context, HostRemoveArgs) (WriteResult, error) {
	return WriteResult{}, errHostsUnavailable
}
func (unavailableHostClient) CreateComponent(context.Context, ComponentCreateArgs) (WriteResult, error) {
	return WriteResult{}, errHostsUnavailable
}
func (unavailableHostClient) UpdateComponent(context.Context, ComponentUpdateArgs) (WriteResult, error) {
	return WriteResult{}, errHostsUnavailable
}
func (unavailableHostClient) MoveComponent(context.Context, ComponentMoveArgs) (WriteResult, error) {
	return WriteResult{}, errHostsUnavailable
}
func (unavailableHostClient) RemoveComponent(context.Context, ComponentRemoveArgs) (WriteResult, error) {
	return WriteResult{}, errHostsUnavailable
}
func (unavailableHostClient) HostHistory(context.Context, string, int) ([]hosts.Change, error) {
	return nil, errHostsUnavailable
}
func (unavailableHostClient) ComponentHistory(context.Context, string, int) ([]hosts.Change, error) {
	return nil, errHostsUnavailable
}

// writeBody is the JSON every write route accepts; each route reads the
// fields it needs. The revision may instead arrive as If-Match.
type writeBody struct {
	Revision     string                  `json:"revision"`
	Acknowledged bool                    `json:"acknowledged"`
	ApprovalID   string                  `json:"approval_id"`
	Patch        map[string]any          `json:"patch"`
	Create       bool                    `json:"create"`
	Component    map[string]any          `json:"component"`
	To           hosts.ComponentLocation `json:"to"`
}

func (b writeBody) args(r *http.Request) WriteArgs {
	rev := b.Revision
	if rev == "" {
		rev = strings.Trim(r.Header.Get("If-Match"), `"`)
	}
	if rev == "" {
		rev = r.URL.Query().Get("revision")
	}
	return WriteArgs{Revision: rev, Acknowledged: b.Acknowledged, ApprovalID: b.ApprovalID}
}

// readWriteBody decodes a write request. Numbers stay exact; unknown fields
// are an error.
func readWriteBody(w http.ResponseWriter, r *http.Request) (writeBody, error) {
	var b writeBody
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return b, &HostInvalidError{Err: redact.Guidance("the request body could not be read")}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return b, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, &HostInvalidError{Err: redact.Guidance("the request body is not valid: %s", err)}
	}
	return b, nil
}

func queryLimit(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return n
}

// hostWriteHTTP serves the host write, history and revision routes. It
// reports whether it handled the request.
func hostWriteHTTP(w http.ResponseWriter, r *http.Request, client HostClient, parts []string) bool {
	wc := HostWriteClientOf(client)
	fail := func(err error) {
		err = scopeError(redact.ScopeFrom(r.Context()), err)
		writeServiceError(w, hostStatus(err), err)
	}
	switch {
	case len(parts) == 1 && parts[0] == "_revision" && r.Method == http.MethodGet:
		rev, err := wc.HostsRevision(r.Context())
		if err != nil {
			fail(err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]string{"revision": rev})
	case len(parts) == 2 && parts[1] == "history" && r.Method == http.MethodGet:
		out, err := wc.HostHistory(r.Context(), unescape(parts[0]), queryLimit(r))
		if err != nil {
			fail(err)
			return true
		}
		writeJSON(w, http.StatusOK, out)
	case len(parts) == 1 && (r.Method == http.MethodPatch || r.Method == http.MethodDelete):
		b, err := readWriteBody(w, r)
		if err != nil {
			fail(err)
			return true
		}
		name := unescape(parts[0])
		var res WriteResult
		if r.Method == http.MethodPatch {
			res, err = wc.UpdateHost(r.Context(), HostUpdateArgs{WriteArgs: b.args(r), Name: name, Patch: b.Patch, Create: b.Create})
		} else {
			res, err = wc.RemoveHost(r.Context(), HostRemoveArgs{WriteArgs: b.args(r), Name: name})
		}
		if err != nil {
			fail(err)
			return true
		}
		writeJSON(w, http.StatusOK, res)
	default:
		return false
	}
	return true
}

// componentWriteHTTP serves the component write, history and revision routes.
func componentWriteHTTP(w http.ResponseWriter, r *http.Request, client ComponentClient, rest string) bool {
	wc := HostWriteClientOf(client)
	parts := []string{}
	if rest != "" {
		parts = strings.Split(rest, "/")
	}
	fail := func(err error) {
		err = scopeError(redact.ScopeFrom(r.Context()), err)
		writeServiceError(w, hostStatus(err), err)
	}
	done := func(res WriteResult, err error) bool {
		if err != nil {
			fail(err)
			return true
		}
		writeJSON(w, http.StatusOK, res)
		return true
	}
	switch {
	case len(parts) == 1 && parts[0] == "_revision" && r.Method == http.MethodGet:
		rev, err := wc.HostsRevision(r.Context())
		if err != nil {
			fail(err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]string{"revision": rev})
		return true
	case len(parts) == 2 && parts[1] == "history" && r.Method == http.MethodGet:
		out, err := wc.ComponentHistory(r.Context(), unescape(parts[0]), queryLimit(r))
		if err != nil {
			fail(err)
			return true
		}
		writeJSON(w, http.StatusOK, out)
		return true
	case r.Method == http.MethodGet:
		return false
	}
	b, err := readWriteBody(w, r)
	if err != nil {
		fail(err)
		return true
	}
	switch {
	case len(parts) == 0 && r.Method == http.MethodPost:
		return done(wc.CreateComponent(r.Context(), ComponentCreateArgs{WriteArgs: b.args(r), Component: b.Component}))
	case len(parts) == 1 && r.Method == http.MethodPatch:
		return done(wc.UpdateComponent(r.Context(), ComponentUpdateArgs{WriteArgs: b.args(r), ID: unescape(parts[0]), Patch: b.Patch}))
	case len(parts) == 1 && r.Method == http.MethodDelete:
		return done(wc.RemoveComponent(r.Context(), ComponentRemoveArgs{WriteArgs: b.args(r), ID: unescape(parts[0])}))
	case len(parts) == 2 && parts[1] == "move" && r.Method == http.MethodPost:
		return done(wc.MoveComponent(r.Context(), ComponentMoveArgs{WriteArgs: b.args(r), ID: unescape(parts[0]), To: b.To}))
	}
	return false
}

// ---- in-process client ----

func (c *InProcessClient) HostsRevision(ctx context.Context) (string, error) {
	s, err := c.hostService()
	if err != nil {
		return "", err
	}
	return s.Revision(ctx)
}
func (c *InProcessClient) UpdateHost(ctx context.Context, a HostUpdateArgs) (WriteResult, error) {
	s, err := c.hostService()
	if err != nil {
		return WriteResult{}, err
	}
	return s.UpdateHost(ctx, a)
}
func (c *InProcessClient) RemoveHost(ctx context.Context, a HostRemoveArgs) (WriteResult, error) {
	s, err := c.hostService()
	if err != nil {
		return WriteResult{}, err
	}
	return s.RemoveHost(ctx, a)
}
func (c *InProcessClient) CreateComponent(ctx context.Context, a ComponentCreateArgs) (WriteResult, error) {
	s, err := c.hostService()
	if err != nil {
		return WriteResult{}, err
	}
	return s.CreateComponent(ctx, a)
}
func (c *InProcessClient) UpdateComponent(ctx context.Context, a ComponentUpdateArgs) (WriteResult, error) {
	s, err := c.hostService()
	if err != nil {
		return WriteResult{}, err
	}
	return s.UpdateComponent(ctx, a)
}
func (c *InProcessClient) MoveComponent(ctx context.Context, a ComponentMoveArgs) (WriteResult, error) {
	s, err := c.hostService()
	if err != nil {
		return WriteResult{}, err
	}
	return s.MoveComponent(ctx, a)
}
func (c *InProcessClient) RemoveComponent(ctx context.Context, a ComponentRemoveArgs) (WriteResult, error) {
	s, err := c.hostService()
	if err != nil {
		return WriteResult{}, err
	}
	return s.RemoveComponent(ctx, a)
}
func (c *InProcessClient) HostHistory(ctx context.Context, name string, limit int) ([]hosts.Change, error) {
	s, err := c.hostService()
	if err != nil {
		return nil, err
	}
	return s.HostHistory(ctx, name, limit)
}
func (c *InProcessClient) ComponentHistory(ctx context.Context, id string, limit int) ([]hosts.Change, error) {
	s, err := c.hostService()
	if err != nil {
		return nil, err
	}
	return s.ComponentHistory(ctx, id, limit)
}

// ---- socket client ----

func (c *SocketClient) HostsRevision(ctx context.Context) (string, error) {
	var out struct {
		Revision string `json:"revision"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/hosts/_revision", nil, &out); err != nil {
		return "", hostRouteError(err)
	}
	return out.Revision, nil
}

func (c *SocketClient) writeCall(ctx context.Context, method, path string, body any) (WriteResult, error) {
	var out WriteResult
	if err := c.doJSON(ctx, method, path, body, &out); err != nil {
		return WriteResult{}, hostRouteError(err)
	}
	return out, nil
}

func (c *SocketClient) UpdateHost(ctx context.Context, a HostUpdateArgs) (WriteResult, error) {
	return c.writeCall(ctx, http.MethodPatch, "/hosts/"+url.PathEscape(a.Name), writeBody{Revision: a.Revision, Acknowledged: a.Acknowledged, ApprovalID: a.ApprovalID, Patch: a.Patch, Create: a.Create})
}
func (c *SocketClient) RemoveHost(ctx context.Context, a HostRemoveArgs) (WriteResult, error) {
	return c.writeCall(ctx, http.MethodDelete, "/hosts/"+url.PathEscape(a.Name), writeBody{Revision: a.Revision, Acknowledged: a.Acknowledged, ApprovalID: a.ApprovalID})
}
func (c *SocketClient) CreateComponent(ctx context.Context, a ComponentCreateArgs) (WriteResult, error) {
	return c.writeCall(ctx, http.MethodPost, "/components", writeBody{Revision: a.Revision, Acknowledged: a.Acknowledged, ApprovalID: a.ApprovalID, Component: a.Component})
}
func (c *SocketClient) UpdateComponent(ctx context.Context, a ComponentUpdateArgs) (WriteResult, error) {
	return c.writeCall(ctx, http.MethodPatch, "/components/"+url.PathEscape(a.ID), writeBody{Revision: a.Revision, Acknowledged: a.Acknowledged, ApprovalID: a.ApprovalID, Patch: a.Patch})
}
func (c *SocketClient) MoveComponent(ctx context.Context, a ComponentMoveArgs) (WriteResult, error) {
	return c.writeCall(ctx, http.MethodPost, "/components/"+url.PathEscape(a.ID)+"/move", writeBody{Revision: a.Revision, Acknowledged: a.Acknowledged, ApprovalID: a.ApprovalID, To: a.To})
}
func (c *SocketClient) RemoveComponent(ctx context.Context, a ComponentRemoveArgs) (WriteResult, error) {
	return c.writeCall(ctx, http.MethodDelete, "/components/"+url.PathEscape(a.ID), writeBody{Revision: a.Revision, Acknowledged: a.Acknowledged, ApprovalID: a.ApprovalID})
}
func (c *SocketClient) HostHistory(ctx context.Context, name string, limit int) ([]hosts.Change, error) {
	var out []hosts.Change
	if err := c.doJSON(ctx, http.MethodGet, "/hosts/"+url.PathEscape(name)+"/history?limit="+strconv.Itoa(limit), nil, &out); err != nil {
		return nil, hostRouteError(err)
	}
	return out, nil
}
func (c *SocketClient) ComponentHistory(ctx context.Context, id string, limit int) ([]hosts.Change, error) {
	var out []hosts.Change
	if err := c.doJSON(ctx, http.MethodGet, "/components/"+url.PathEscape(id)+"/history?limit="+strconv.Itoa(limit), nil, &out); err != nil {
		return nil, hostRouteError(err)
	}
	return out, nil
}
