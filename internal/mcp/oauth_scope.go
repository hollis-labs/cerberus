package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hollis-labs/cerberus/internal/oauth"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// mcp-http's half of WP-S8: a verified token's scopes are checked before a
// call is forwarded, so a daemon too old to check them itself still never
// runs a call the token does not cover. The daemon checks again.

// pluginToolEffects are the effects of the plugin tools being served, by
// tool name.
var pluginToolEffects sync.Map

// ToolEffect is the effect of the operation a tool runs.
func ToolEffect(name string) (contract.Effect, bool) {
	if op, ok := ToolOperation(name); ok {
		return op.Effect, true
	}
	if e, ok := pluginToolEffects.Load(name); ok {
		return e.(contract.Effect), true
	}
	return "", false
}

type bearerKey struct{}

// BearerFromContext is the bearer token of the call ctx serves, for the
// socket client to forward.
func BearerFromContext(ctx context.Context) string {
	tok, _ := ctx.Value(bearerKey{}).(string)
	return tok
}

// TokenKey is where a TokenVerifier leaves the raw token in TokenInfo.Extra,
// for ScopeMiddleware to carry on to the daemon.
const TokenKey = "cerberus.bearer"

// ScopeMiddleware refuses a tool call the caller's token does not cover,
// with a result whose text says what it lacks (a client may show the
// model only the text), and carries the token on to the daemon.
func ScopeMiddleware(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		call, ok := req.(*mcpsdk.CallToolRequest)
		if !ok || call.Extra == nil || call.Extra.TokenInfo == nil {
			return next(ctx, method, req)
		}
		info := call.Extra.TokenInfo
		if tok, _ := info.Extra[TokenKey].(string); tok != "" {
			ctx = context.WithValue(ctx, bearerKey{}, tok)
		}
		name := ""
		if call.Params != nil {
			name = call.Params.Name
		}
		effect, known := ToolEffect(name)
		if !known {
			effect = contract.EffectExec
		}
		if oauth.Allows(info.Scopes, effect) {
			return next(ctx, method, req)
		}
		need := oauth.RequiredScope(effect)
		client, _ := info.Extra["client"].(string)
		if client == "" {
			client = "<client>"
		}
		text := fmt.Sprintf("insufficient_scope: this token's scopes (%s) do not cover %s, a %s operation, which needs %s. Nothing ran.\n"+
			"next_step: you cannot widen a token yourself; ask your operator for one with %s (`cerberus mcp-http token issue --client %s --scope %s` on their terminal).",
			strings.Join(info.Scopes, " "), name, effect, need, need, client, need)
		return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}}}, nil
	}
}
