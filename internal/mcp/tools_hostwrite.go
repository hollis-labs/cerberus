package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// The host and component writes, their history and the component reads: main's
// tools_host_write.go and tools_component.go over /api/hosts and /api/components.

func init() {
	for name, h := range map[string]handler{
		"cerberus_host_revision":     hostRevision,
		"cerberus_host_update":       hostUpdateTool,
		"cerberus_host_inventory":    hostInventoryTool,
		"cerberus_host_remove":       hostWriteTool(http.MethodDelete, "name", "/hosts/", ""),
		"cerberus_host_history":      historyTool("/hosts/", "name"),
		"cerberus_component_create":  componentCreateTool,
		"cerberus_component_update":  componentUpdateTool,
		"cerberus_component_move":    componentMoveTool,
		"cerberus_component_remove":  hostWriteTool(http.MethodDelete, "id", "/components/", ""),
		"cerberus_component_history": historyTool("/components/", "id"),
		"cerberus_component_list":    componentListTool,
		"cerberus_component_get":     componentGetTool,
	} {
		handlers[name] = h
	}
}

// writeArgs are the fields every write sends (main's wargs).
func writeArgs(args map[string]any) map[string]any {
	body := map[string]any{"revision": str(args, "revision"), "acknowledged": boolArg(args, "acknowledged")}
	if a := str(args, "approval_id"); a != "" {
		body["approval_id"] = a
	}
	return body
}

func object(args map[string]any, key string) map[string]any {
	m, _ := args[key].(map[string]any)
	return m
}

// rawIndented is a route's answer as indented redacted JSON.
func rawIndented(ctx context.Context, b Backend, method, path string, body any) (any, error) {
	var out json.RawMessage
	if err := b.Do(ctx, method, path, body, &out); err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	return indented(v)
}

func hostRevision(ctx context.Context, b Backend, _ map[string]any) (any, error) {
	var out struct {
		Revision string `json:"revision"`
	}
	if err := b.Do(ctx, http.MethodGet, "/hosts/_revision", nil, &out); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(map[string]string{"revision": out.Revision})
	return string(data), nil
}

func hostUpdateTool(ctx context.Context, b Backend, args map[string]any) (any, error) {
	body := writeArgs(args)
	body["patch"], body["create"] = object(args, "patch"), boolArg(args, "create")
	return rawIndented(ctx, b, http.MethodPatch, "/hosts/"+url.PathEscape(str(args, "name")), body)
}

func hostInventoryTool(ctx context.Context, b Backend, args map[string]any) (any, error) {
	body := writeArgs(args)
	body["dry_run"] = boolArg(args, "dry_run")
	return rawIndented(ctx, b, http.MethodPost, "/hosts/"+url.PathEscape(str(args, "name"))+"/inventory", body)
}

// hostWriteTool is a write on prefix+<arg> with the write fields alone.
func hostWriteTool(method, arg, prefix, suffix string) handler {
	return func(ctx context.Context, b Backend, args map[string]any) (any, error) {
		return rawIndented(ctx, b, method, prefix+url.PathEscape(str(args, arg))+suffix, writeArgs(args))
	}
}

func historyTool(prefix, arg string) handler {
	return func(ctx context.Context, b Backend, args map[string]any) (any, error) {
		q := url.Values{}
		if n, ok := args["limit"].(float64); ok {
			q.Set("limit", fmt.Sprint(int(n)))
		}
		return rawIndented(ctx, b, http.MethodGet, withQuery(prefix+url.PathEscape(str(args, arg))+"/history", q), nil)
	}
}

func componentCreateTool(ctx context.Context, b Backend, args map[string]any) (any, error) {
	body := writeArgs(args)
	body["component"] = object(args, "component")
	return rawIndented(ctx, b, http.MethodPost, "/components", body)
}

func componentUpdateTool(ctx context.Context, b Backend, args map[string]any) (any, error) {
	body := writeArgs(args)
	body["patch"] = object(args, "patch")
	return rawIndented(ctx, b, http.MethodPatch, "/components/"+url.PathEscape(str(args, "id")), body)
}

func componentMoveTool(ctx context.Context, b Backend, args map[string]any) (any, error) {
	body := writeArgs(args)
	body["to"] = object(args, "to")
	return rawIndented(ctx, b, http.MethodPost, "/components/"+url.PathEscape(str(args, "id"))+"/move", body)
}

func componentListTool(ctx context.Context, b Backend, args map[string]any) (any, error) {
	q := url.Values{}
	for _, k := range []string{"kind", "host"} {
		if v := str(args, k); v != "" {
			q.Set(k, v)
		}
	}
	if boolArg(args, "spare") {
		q.Set("spare", "true")
	}
	var comps []json.RawMessage
	if err := b.Do(ctx, http.MethodGet, withQuery("/components", q), nil, &comps); err != nil {
		return nil, err
	}
	var envelope any
	if err := json.Unmarshal([]byte(budgetedList("cerberus_component_list", comps, args, "%d components total.")), &envelope); err != nil {
		return nil, err
	}
	return withNotes(envelope, nil)
}

func componentGetTool(ctx context.Context, b Backend, args map[string]any) (any, error) {
	return rawIndented(ctx, b, http.MethodGet, "/components/"+url.PathEscape(str(args, "id")), nil)
}
