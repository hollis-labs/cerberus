package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/gate"
)

// The approvals routes are the old socket's /approvals routes (internal/cerbapi
// socket_server.go handleApprovals on main) with the old bodies: the list as gate.ApprovalList,
// one approval and every decision's answer as approval.Approval. Errors use the flat body, with
// the old statuses (gate.ApprovalErrorStatus). The passkey routes (/approvals/keys, challenge)
// are gone with passkeys (decision 0.1).

// broker is the gate's approvals broker, or false after answering that there is none.
func (a *API) broker(w http.ResponseWriter) (*gate.Broker, bool) {
	if a.o.Gate == nil || a.o.Gate.Broker() == nil {
		apierr.Write(w, http.StatusServiceUnavailable, "approvals_unavailable",
			"this Cerberus holds no approvals: in observe mode the old Cerberus holds them until the handover", nil)
		return nil, false
	}
	return a.o.Gate.Broker(), true
}

func (a *API) approvalList(w http.ResponseWriter, _ *http.Request) {
	b, ok := a.broker(w)
	if !ok {
		return
	}
	writeJSON(w, gate.ApprovalList{Approvals: b.List(), Problems: b.Problems()})
}

func (a *API) approvalGet(w http.ResponseWriter, r *http.Request) {
	b, ok := a.broker(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	got, found := b.Get(id)
	if !found {
		apierr.Write(w, http.StatusNotFound, "not_found", fmt.Sprintf("no approval %q", id), nil)
		return
	}
	writeJSON(w, got)
}

// approvalDecide decides a pending approval as the owner's console session: personOnly has
// already refused every other caller, and the owner session is what DecideAs puts on an
// out-of-band approve as its proof.
func (a *API) approvalDecide(w http.ResponseWriter, r *http.Request) {
	var args gate.ApprovalDecisionArgs
	a.approvalAction(w, r, &args, func(b *gate.Broker, id string) (approval.Approval, error) {
		p, _ := principalFrom(r)
		return b.DecideAs(GateContext(r.Context()), id, args, gate.OwnerSession{ConsolePerson: IsConsolePerson(p), SessionID: p.SessionID})
	})
}

func (a *API) approvalRevoke(w http.ResponseWriter, r *http.Request) {
	var args gate.ApprovalRevokeArgs
	a.approvalAction(w, r, &args, func(b *gate.Broker, id string) (approval.Approval, error) {
		return b.RevokeAs(GateContext(r.Context()), id, args)
	})
}

func (a *API) approvalAckBreakGlass(w http.ResponseWriter, r *http.Request) {
	var args struct {
		Note string `json:"note"`
	}
	a.approvalAction(w, r, &args, func(b *gate.Broker, id string) (approval.Approval, error) {
		return b.AckBreakGlassAs(GateContext(r.Context()), id, args.Note)
	})
}

// approvalAction decodes the body into args as the old socket did (empty is the zero value,
// at most 1 MiB), runs do and answers with the approval or the old status.
func (a *API) approvalAction(w http.ResponseWriter, r *http.Request, args any, do func(*gate.Broker, string) (approval.Approval, error)) {
	b, ok := a.broker(w)
	if !ok {
		return
	}
	if err := decodeBody(r.Body, args); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
		return
	}
	id := r.PathValue("id")
	got, err := do(b, id)
	if err != nil {
		status, msg := gate.ApprovalErrorStatus(err, id)
		apierr.Write(w, status, approvalErrorCode(err, status), msg, nil)
		return
	}
	writeJSON(w, got)
}

// approvalErrorCode names a decision's refusal for the flat body. The old socket sent only the
// text; the status is the old one.
func approvalErrorCode(err error, status int) string {
	switch {
	case errors.Is(err, approval.ErrNotFound):
		return "not_found"
	case errors.Is(err, approval.ErrExpired):
		return "approval_expired"
	case errors.Is(err, approval.ErrNotPending):
		return "approval_not_pending"
	case errors.Is(err, approval.ErrNotApproved):
		return "approval_not_approved"
	case errors.Is(err, approval.ErrNotBreakGlass):
		return "not_break_glass"
	case errors.Is(err, approval.ErrNoPresence):
		return "owner_session_required"
	case status == http.StatusForbidden:
		return "approver_refused"
	}
	return "internal"
}

// decodeBody is the old socket's decodeJSONBody.
func decodeBody(body io.Reader, dst any) error {
	if body == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode body: %w", err)
	}
	return nil
}
