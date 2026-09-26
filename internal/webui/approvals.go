package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// approvalsClient is the daemon's approvals as the console reaches them.
// The console's client is the socket client; a client without these (the
// in-process client in a test) answers 503.
type approvalsClient interface {
	ListApprovals(ctx context.Context) (cerbapi.ApprovalList, error)
	GetApproval(ctx context.Context, id string) (approval.Approval, error)
	DecideApproval(ctx context.Context, id string, args cerbapi.ApprovalDecisionArgs) (approval.Approval, error)
	RevokeApproval(ctx context.Context, id string, args cerbapi.ApprovalRevokeArgs) (approval.Approval, error)
}

func (s *Server) approvals(w http.ResponseWriter) (approvalsClient, bool) {
	c, ok := s.client.(approvalsClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "this console is not connected to a daemon that holds approvals")
	}
	return c, ok
}

// handleApprovals is GET /api/approvals: every approval, newest first, with
// who asked and the plan each would run.
func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	c, ok := s.approvals(w)
	if !ok {
		return
	}
	list, err := c.ListApprovals(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// consoleDecision is what the approvals page sends: approve or deny, the
// target typed to confirm an approve, and a reason.
type consoleDecision struct {
	Approve   bool            `json:"approve"`
	Typed     string          `json:"typed,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Assertion json.RawMessage `json:"assertion,omitempty"`
}

// handleApprovalByID is GET /api/approvals/{id}, and POST
// /api/approvals/{id}/decide and /revoke on a signed-in session with its
// action token. An approve is confirmed by typing the target, as on a
// terminal, and an out-of-band approve carries the passkey assertion made
// for it. The daemon decides whether it counts: the console's session is
// the decider, a request made through the console cannot be approved here,
// and an out-of-band request counts only with a verified passkey assertion.
func (s *Server) handleApprovalByID(w http.ResponseWriter, r *http.Request) {
	// Anything but a read passes the state-change guard before anything
	// else is looked at, a missing id included.
	if r.Method != http.MethodGet && !s.allowStateChangingRequest(r) {
		writeError(w, http.StatusForbidden, "state-changing request rejected")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/approvals/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "keys" {
		s.handlePasskeys(w, r, action)
		return
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "approval id required")
		return
	}
	c, ok := s.approvals(w)
	if !ok {
		return
	}
	if action == "" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a, err := c.GetApproval(r.Context(), id)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if action == "challenge" {
		s.handleApprovalChallenge(w, r, id)
		return
	}
	var body consoleDecision
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch action {
	case "decide":
		if body.Approve {
			current, err := c.GetApproval(r.Context(), id)
			if err != nil {
				writeClientError(w, err)
				return
			}
			if want := approvalTargetName(current); strings.TrimSpace(body.Typed) != want {
				writeError(w, http.StatusBadRequest, "type the target ("+want+") to approve; nothing was approved")
				return
			}
		}
		a, err := c.DecideApproval(r.Context(), id, cerbapi.ApprovalDecisionArgs{Approve: body.Approve, Reason: body.Reason, Assertion: body.Assertion})
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	case "revoke":
		a, err := c.RevokeApproval(r.Context(), id, cerbapi.ApprovalRevokeArgs{Reason: body.Reason})
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	default:
		writeError(w, http.StatusNotFound, "unknown approval action "+action)
	}
}

// approvalTargetName is what an approver types: the resource the target was
// resolved through, or its kind.
func approvalTargetName(a approval.Approval) string {
	if a.Target.Resource != "" {
		return a.Target.Resource
	}
	return a.Target.Kind
}
