package mcp

import (
	"context"
	"encoding/json"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type clientKey struct{}

// ClientFrom is the MCP client a tool call came from: its clientInfo name, from the call's _meta
// or, failing that, the session's initialize (main's order). Unverified: a label, never identity.
func ClientFrom(ctx context.Context) string {
	s, _ := ctx.Value(clientKey{}).(string)
	return s
}

// captureClient puts the calling MCP client on a tools/call's context, for the backend to carry
// to the gate (main's mcpPrincipal).
func captureClient(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		if method == "tools/call" {
			ctx = context.WithValue(ctx, clientKey{}, clientName(req))
		}
		return next(ctx, method, req)
	}
}

func clientName(req mcpsdk.Request) string {
	if p := req.GetParams(); p != nil {
		if raw, ok := p.GetMeta()[mcpsdk.MetaKeyClientInfo]; ok {
			var info struct {
				Name string `json:"name"`
			}
			if data, err := json.Marshal(raw); err == nil && json.Unmarshal(data, &info) == nil && info.Name != "" {
				return info.Name
			}
		}
	}
	if ss, ok := req.GetSession().(*mcpsdk.ServerSession); ok {
		if ip := ss.InitializeParams(); ip != nil && ip.ClientInfo != nil {
			return ip.ClientInfo.Name
		}
	}
	return ""
}
