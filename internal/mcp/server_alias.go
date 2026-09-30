package mcp

import (
	"context"

	gmcp "github.com/hollis-labs/go-mcp/server"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type Tool = gmcp.Tool
type ToolHandler = gmcp.ToolHandler
type Server = gmcp.Server

type Option = gmcp.Option

func NewServer(name, version string, opts ...Option) *gmcp.Server {
	srv := gmcp.NewServer(name, version, opts...)
	srv.SDKServer().AddSendingMiddleware(announceToolsOnSubscribe)
	srv.SDKServer().AddReceivingMiddleware(markEgress)
	return srv
}

const (
	notificationSubscriptionsAck = "notifications/subscriptions/acknowledged"
	notificationToolListChanged  = "notifications/tools/list_changed"
)

// announceToolsOnSubscribe tells a session its tool list may have changed
// as soon as its subscription is acknowledged (CERB-GAP-888). The go-sdk
// client opens subscriptions/listen without waiting for it, so Connect
// returns before the server has registered the subscription, and a change
// made in that window, such as a plugin tool the sync exposes just then,
// notifies no one. The acknowledgment goes out once the subscription is
// registered, so a list_changed sent after it reaches the session, which
// lists again and sees whatever it missed. It costs one extra list per
// connecting client.
func announceToolsOnSubscribe(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil || method != notificationSubscriptionsAck {
			return res, err
		}
		// Read through the interface: the SDK sends the acknowledgment as a
		// ServerRequest of the Params interface, not of its concrete type.
		ack, ok := req.GetParams().(*mcpsdk.SubscriptionsAcknowledgedParams)
		session, isServer := req.GetSession().(*mcpsdk.ServerSession)
		if !ok || ack == nil || !isServer || !ack.Notifications.ToolsListChanged {
			return res, err
		}
		// The subscription id the client demultiplexes on, as the SDK's own
		// list_changed carries it.
		params := &mcpsdk.ToolListChangedParams{Meta: mcpsdk.Meta{mcpsdk.MetaKeySubscriptionID: ack.Meta[mcpsdk.MetaKeySubscriptionID]}}
		_, _ = next(ctx, notificationToolListChanged, &mcpsdk.ServerRequest[*mcpsdk.ToolListChangedParams]{Session: session, Params: params})
		return res, err
	}
}

func emptyObjectSchema() map[string]interface{} {
	return gmcp.EmptyObjectSchema()
}

func objectSchema(properties map[string]interface{}, required ...string) map[string]interface{} {
	return gmcp.ObjectSchema(properties, required...)
}
