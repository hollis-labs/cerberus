package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/policy"
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
	if sess := webSession(r.Context()); sess != nil && sess.scope != "" {
		// A session from an approval link sees that approval alone (M7).
		mine := list.Approvals[:0:0]
		for _, a := range list.Approvals {
			if a.ID == sess.scope {
				mine = append(mine, a)
			}
		}
		list.Approvals, list.Problems = mine, nil
	}
	writeJSON(w, http.StatusOK, list)
}

// scopedRouteAllowed is what a session from an approval link may reach
// (M7): the session itself (for its action token), sign-out, the approvals
// list (filtered to its approval), and its one approval: read, passkey
// challenge and decision. Nothing else, so the link an MCP client is handed
// is not a console login.
func scopedRouteAllowed(r *http.Request, id string) bool {
	base := "/api/approvals/" + id
	switch r.Method {
	case http.MethodGet:
		return r.URL.Path == "/api/session" || r.URL.Path == "/api/approvals" || r.URL.Path == base
	case http.MethodPost:
		return r.URL.Path == "/api/logout" || r.URL.Path == base+"/challenge" || r.URL.Path == base+"/decide"
	}
	return false
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
		if sess := webSession(r.Context()); sess != nil && sess.scope != "" && body.Approve && body.Assertion == nil {
			writeError(w, http.StatusForbidden, "a sign-in from an approval link approves only with a passkey; nothing was approved. Run `cerberus approvals approve "+id+"` in a terminal to decide it there")
			return
		}
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

// breakGlassAlert is the header's break-glass badge (P3-5b): the uses in the
// last day, and the follow-ups still open, which stay until acknowledged.
// Nil when there are none, or the daemon cannot say.
func (s *Server) breakGlassAlert(ctx context.Context) map[string]any {
	c, ok := s.client.(approvalsClient)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	list, err := c.ListApprovals(ctx)
	if err != nil {
		return nil
	}
	since := time.Now().Add(-24 * time.Hour)
	recent, open := 0, 0
	for _, a := range list.Approvals {
		if a.BreakGlass == nil || a.Status != approval.Consumed {
			continue
		}
		if a.ConsumedAt.After(since) {
			recent++
		}
		if a.BreakGlass.AckedAt.IsZero() {
			open++
		}
	}
	if recent == 0 && open == 0 {
		return nil
	}
	return map[string]any{"recent": recent, "open": open,
		"summary": fmt.Sprintf("BREAK GLASS: %d in the last day, %d to acknowledge (`cerberus approvals ack-break-glass`)", recent, open)}
}

// enforcementAlert is the header's enforcement line (P3-7): what is
// enforced, and, loudly, a snapshot mismatch and the path it took.
func enforcementAlert() map[string]any {
	e, ok := cerbapi.PolicyDecisionPoint().(interface {
		Enforcement() (policy.Enforcement, string)
	})
	if !ok {
		return nil
	}
	enf, note := e.Enforcement()
	return map[string]any{"summary": enf.Summary(), "enforced": enf.Mode == policy.EnforceAll || len(enf.Enforce) > 0, "mismatch": note}
}
