package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/hollis-labs/cerberus/internal/gate"
)

// Backend calls the serving process's /api. path is after /api ("/resources/x/status"); in, when
// not nil, is the JSON body; out receives the JSON answer. A non-2xx answer is a *Refusal.
type Backend interface {
	Do(ctx context.Context, method, path string, in, out any) error
}

// Refusal is a non-2xx answer from /api, with its flat body {code, message, details.approval}.
type Refusal struct {
	Status        int
	Code, Message string
	Approval      *gate.ApprovalRef
}

func (r *Refusal) Error() string {
	if r.Message == "" {
		return fmt.Sprintf("the server answered %d", r.Status)
	}
	return r.Message
}

// DecodeAnswer turns an /api answer into out or a *Refusal.
func DecodeAnswer(status int, data []byte, out any) error {
	if status/100 != 2 {
		var flat struct {
			Code, Message string
			Details       struct {
				Approval *gate.ApprovalRef `json:"approval"`
			}
		}
		_ = json.Unmarshal(data, &flat)
		return &Refusal{Status: status, Code: flat.Code, Message: flat.Message, Approval: flat.Details.Approval}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// MuxBackend serves the calls in-process through the API's own handlers (api.API.Mux), under the
// caller's context, so the principal the MCP transport stamped is the principal the route sees:
// the gate, approvals and the person check behave as they do over HTTP. The route scopes are not
// checked here; each tool's scope is (Register).
type MuxBackend struct{ Handler http.Handler }

// Do runs one call through the handler.
func (b MuxBackend) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "/api"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	b.Handler.ServeHTTP(rec, req)
	return DecodeAnswer(rec.status, rec.body.Bytes(), out)
}

// recorder is the in-process response.
type recorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	r.wrote = true
	return r.body.Write(p)
}
