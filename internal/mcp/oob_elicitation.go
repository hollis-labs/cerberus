package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	gmcp "github.com/hollis-labs/libs/plugin-mcp/go-mcp/server"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// Out-of-band approval through the client (P4-6). When a call needs an
// out-of-band approval and the client can open a URL for its user (URL-mode
// elicitation, protocol 2026-07-28), the answer is not approval_pending but
// an input request: open the console's page for this approval. The passkey
// is still the approval; the elicitation only takes the operator there.
//
// On the retry the client makes once the page was opened, the tool waits a
// bounded time for the decision. Approved, it runs the call again with the
// approval id, so consume binds it to the requester, the arguments and the
// plan exactly as a retry by hand would. Anything else answers
// approval_pending for the same approval, never a new one.

// ConsoleApprovalURL is a one-time sign-in link to the console's page for an
// approval, set by the process serving MCP; nil, or an error, means no
// console to send the operator to, and the call answers approval_pending.
var ConsoleApprovalURL func(approvalID string) (string, error)

// oobElicitWait is how long a retry waits for the operator's decision after
// the page was opened. Tests shorten it.
var oobElicitWait = 60 * time.Second

const oobRequestID = "cerberus_approval"

// stateKey signs the retry state. The state comes back through the client;
// the key never leaves this process, so a client cannot point a retry at an
// approval it was not sent to. It is not what makes the call safe: consume
// binds the approval to its requester, arguments and plan.
var stateKey = func() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}()

type oobState struct {
	Approval string `json:"a"`
	Tool     string `json:"t"`
	MAC      []byte `json:"m"`
}

func mac(approvalID, tool string) []byte {
	h := hmac.New(sha256.New, stateKey)
	h.Write([]byte("cerberus oob elicitation v1\x00" + approvalID + "\x00" + tool))
	return h.Sum(nil)
}

func signState(approvalID, tool string) string {
	data, _ := json.Marshal(oobState{Approval: approvalID, Tool: tool, MAC: mac(approvalID, tool)})
	return base64.RawURLEncoding.EncodeToString(data)
}

func verifyState(state, tool string) (string, bool) {
	data, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return "", false
	}
	var s oobState
	if json.Unmarshal(data, &s) != nil || s.Tool != tool || !hmac.Equal(s.MAC, mac(s.Approval, s.Tool)) {
		return "", false
	}
	return s.Approval, true
}

// withOutOfBandElicitation wraps a gated tool (one that takes approval_id).
func withOutOfBandElicitation(client cerbapi.Client, tool Tool) Tool {
	props, _ := tool.InputSchema.(map[string]any)
	if p, _ := props["properties"].(map[string]any); p[argApprovalID] == nil {
		return tool
	}
	reader, _ := client.(approvalReader)
	handler := tool.Handler
	tool.Handler = func(ctx context.Context, args map[string]any) (any, error) {
		if state := gmcp.RequestState(ctx); state != "" {
			return retryAfterElicitation(ctx, reader, tool.Name, handler, args, state)
		}
		result, err := handler(ctx, args)
		if ref, ok := outOfBandPending(err); ok && reader != nil && urlElicitation(ctx) && ConsoleApprovalURL != nil {
			if link, linkErr := ConsoleApprovalURL(ref.ID); linkErr == nil {
				return gmcp.InputRequired{
					Requests: mcpsdk.InputRequestMap{oobRequestID: &mcpsdk.ElicitParams{
						Mode:          "url",
						URL:           link,
						ElicitationID: ref.ID,
						Message:       redact.Guidance("%s needs your approval with a passkey (Touch ID or a security key). Open the Cerberus console to review the plan and approve %s.", tool.Name, ref.ID).Error(),
					}},
					State: signState(ref.ID, tool.Name),
				}, nil
			}
		}
		return result, err
	}
	return tool
}

// outOfBandPending is err's approval, when it is approval_pending for an
// out-of-band approval with an id.
func outOfBandPending(err error) (*cerbapi.ApprovalRef, bool) {
	var ref *cerbapi.ApprovalRef
	var coded *cerbapi.ExternalConnectorError
	var failure toolFailure
	switch {
	case errors.As(err, &coded) && coded.Code == cerbapi.ExternalConnectorApprovalPending:
		ref = coded.Approval
	case errors.As(err, &failure):
		// A connector tool relays the refusal as its DTO (connectorFailure).
		if r, ok := failure.content.(toolRefusal); ok && r.Code == string(cerbapi.ExternalConnectorApprovalPending) {
			ref = r.Approval
		}
	}
	if ref == nil || ref.ID == "" || ref.Channel != approval.ChannelOutOfBand {
		return nil, false
	}
	return ref, true
}

// urlElicitation is whether the calling client can open a URL for its user.
func urlElicitation(ctx context.Context) bool {
	caps := gmcp.ClientCapabilities(ctx)
	return caps != nil && caps.Elicitation != nil && caps.Elicitation.URL != nil
}

// retryAfterElicitation is the client's retry once it has put the console
// page in front of the operator.
func retryAfterElicitation(ctx context.Context, reader approvalReader, tool string, handler ToolHandler, args map[string]any, state string) (any, error) {
	id, ok := verifyState(state, tool)
	if !ok || reader == nil {
		return nil, redact.Guidance("the retry's state does not verify, so nothing ran; call %s again without it", tool)
	}
	resp, _ := gmcp.InputResponses(ctx)[oobRequestID].(*mcpsdk.ElicitResult)
	if resp != nil && resp.Action == "accept" {
		if a, err := awaitDecision(ctx, reader, id); err == nil && a.Status == approval.Approved {
			retry := make(map[string]any, len(args)+1)
			for k, v := range args {
				retry[k] = v
			}
			retry[argApprovalID] = id
			return handler(ctx, retry)
		}
	}
	// Not approved, or not yet: the same approval, still pending, with how
	// to wait for it.
	a, err := reader.GetApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	return nil, pendingAgain(a)
}

// awaitDecision waits, bounded, for the approval to leave pending.
func awaitDecision(ctx context.Context, reader approvalReader, id string) (approval.Approval, error) {
	deadline := time.NewTimer(oobElicitWait)
	defer deadline.Stop()
	tick := time.NewTicker(approvalPollInterval)
	defer tick.Stop()
	for {
		a, err := reader.GetApproval(ctx, id)
		if err != nil || a.Status != approval.Pending {
			return a, err
		}
		select {
		case <-ctx.Done():
			return a, ctx.Err()
		case <-deadline.C:
			return a, nil
		case <-tick.C:
		}
	}
}

// pendingAgain is the answer for an approval already asked for that did not
// come back approved: still pending (wait for it), or decided otherwise.
func pendingAgain(a approval.Approval) error {
	e := &cerbapi.ExternalConnectorError{Connector: a.Connector, Operation: a.Operation}
	switch a.Status {
	case approval.Pending:
		e.Code = cerbapi.ExternalConnectorApprovalPending
		e.Approval = &cerbapi.ApprovalRef{ID: a.ID, ExpiresAt: a.ExpiresAt, ApproveWith: a.ApproveWith(), Channel: a.Channel}
		e.Err = redact.Guidance("approval %s was not decided while the console page was open; it is still pending", a.ID)
	case approval.Expired:
		e.Code = cerbapi.ExternalConnectorApprovalExpired
		e.Err = redact.Guidance("approval %s expired unused", a.ID)
	default:
		e.Code = cerbapi.ExternalConnectorApprovalRequired
		e.Err = redact.Guidance("approval %s is %s, so nothing ran", a.ID, a.Status)
	}
	return e
}
