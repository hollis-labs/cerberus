package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// consoleWriteBody is a console write on the socket: the write, and the
// call's options. A confirm route carries the hash of the plan confirmed.
type consoleWriteBody struct {
	Request ConsoleWriteRequest `json:"request"`
	MutationOpts
	ConfirmedPlanHash string `json:"confirmed_plan_hash,omitempty"`
}

// ConsoleWrite asks the daemon to make a console write; a confirmed one
// goes to its confirm route.
func (c *SocketClient) ConsoleWrite(ctx context.Context, req ConsoleWriteRequest, options ...MutationOption) (*ConsoleWriteResult, error) {
	var out ConsoleWriteResult
	if err := c.consoleWriteCall(ctx, req, options, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PlanConsoleWrite asks the daemon for a console write's plan.
func (c *SocketClient) PlanConsoleWrite(ctx context.Context, req ConsoleWriteRequest, options ...MutationOption) (*ConnectorPlan, error) {
	var out ConnectorPlan
	if err := c.consoleWriteCall(ctx, req, append(append([]MutationOption(nil), options...), WithPlan()), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) consoleWriteCall(ctx context.Context, req ConsoleWriteRequest, options []MutationOption, out any) error {
	if req.Operation == "" {
		return errors.New("console write operation required")
	}
	opts := ApplyMutationOptions(options)
	path := "/console/" + url.PathEscape(req.Operation)
	switch {
	case opts.ConfirmedPlanHash != "":
		path += "/confirm"
	case opts.Plan:
		path += "/plan"
	}
	body := consoleWriteBody{Request: req, MutationOpts: opts, ConfirmedPlanHash: opts.ConfirmedPlanHash}
	// Not streamed: a write is quick, and a refusal keeps its status (a
	// missing profile is 404, a refused path 400) only outside a stream.
	err := c.doJSON(ctx, http.MethodPost, path, body, out)
	if err != nil && strings.Contains(err.Error(), "404 page not found") {
		// A daemon that predates console writes has no such route.
		return redact.GuidanceWrap(err, "the running daemon predates making the console's writes itself, so it refused and nothing was written; restart the daemon on this build, then retry")
	}
	return err
}

// handleConsoleWrite is POST /console/{operation}, /plan and /confirm.
func (s *SocketServer) handleConsoleWrite(w http.ResponseWriter, r *http.Request) {
	operation, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/console/"), "/")
	route := routeRun
	switch action {
	case "":
	case "plan":
		route = routePlan
	case "confirm":
		route = routeConfirm
	default:
		writeJSONError(w, http.StatusNotFound, "expected /console/{operation}, /plan or /confirm")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read console write body")
		return
	}
	var body consoleWriteBody
	if jerr := json.Unmarshal(data, &body); jerr != nil {
		// Not the decoder's text: the body can carry a credential.
		writeJSONError(w, http.StatusBadRequest, "the console write body is not the JSON this daemon expects")
		return
	}
	body.Request.Operation = operation
	opts := []MutationOption{WithApprovalID(body.ApprovalID)}
	switch route {
	case routeConfirm:
		if body.ConfirmedPlanHash == "" {
			writeJSONError(w, http.StatusBadRequest, errConfirmWithoutHash.Error())
			return
		}
		opts = append(opts, WithConfirmedPlanHash(body.ConfirmedPlanHash))
	case routePlan:
		opts = append(opts, WithPlan())
	case routeRun, routeBreakGlass:
	}
	var out any
	if route == routePlan {
		out, err = s.client.PlanConsoleWrite(r.Context(), body.Request, opts...)
	} else {
		out, err = s.client.ConsoleWrite(r.Context(), body.Request, opts...)
	}
	if err != nil {
		writeConsoleWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// writeConsoleWriteError answers a failed console write: not found as 404,
// a refused input as 400, a gate refusal with its own status.
func writeConsoleWriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrConsoleWriteNotFound):
		writeJSONError(w, http.StatusNotFound, err.Error())
	case errors.As(err, new(ConsoleWriteInputError)):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	default:
		writeServiceError(w, http.StatusInternalServerError, fmt.Errorf("console write: %w", err))
	}
}
