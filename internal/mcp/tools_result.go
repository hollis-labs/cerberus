package mcp

import (
	"encoding/json"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// lifecycleResult preserves the JSON response shape shared by many MCP tools.
type lifecycleResult = cerbapi.OpResult

func marshalResult(r lifecycleResult) string {
	data, _ := redact.MarshalIndent(r, "", "  ")
	return string(data)
}

// toolResult is how a tool returns an OpResult. A result that did not
// succeed — a refusal, a missing argument, a failed operation — is returned
// as a tool error, so the client sees isError:true. The body is the same
// redacted OpResult either way; only the error flag differs. A success:false
// body with no isError reads as a successful call to a client that checks
// the flag, which is what every MCP client is entitled to do.
func toolResult(r lifecycleResult) (any, error) {
	if !r.Success {
		msg := r.Error
		if msg == "" {
			msg = r.Message
		}
		return nil, toolFailure{message: msg, content: r}
	}
	return marshalResult(r), nil
}

// toolFailure is a tool error whose structured content is a Cerberus DTO.
// go-mcp reports it with isError:true and marshals ToolErrorContent as the
// result body, in place of the bare message.
type toolFailure struct {
	message string
	content any
}

func (f toolFailure) Error() string { return redact.Text(f.message) }

// ToolErrorContent returns the body already redacted: go-mcp marshals it
// with encoding/json, which would skip Cerberus's redaction.
func (f toolFailure) ToolErrorContent() any {
	data, err := redact.Marshal(f.content)
	if err != nil {
		data, _ = json.Marshal(map[string]any{"success": false, "error": f.Error()})
	}
	return json.RawMessage(data)
}
