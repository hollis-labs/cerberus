package api

import (
	"net/http"

	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/gate"
)

// The audit lane's routes (slice 7d): `cerberus audit prune` and `cerberus audit reanchor`,
// which the old build ran in the CLI's own process. Both write the audit chain, so they run in
// the serving Cerberus on the gate's own sink, the chain's one writer (gate/audit_lane.go):
//
//	POST /api/audit/prune/plan       {before}                              AuditPrunePlan
//	POST /api/audit/prune            {before, acknowledged, approval_id}   AuditPruneResult
//	POST /api/audit/reanchor/plan    {}                                    AuditReanchorPlan
//	POST /api/audit/reanchor         {acknowledged, approval_id}           AuditReanchorResult
//
// They need cerberus.admin, and they are the owner's: the owner signed in on the console, or a
// key naming with approval_id the out-of-band approval its first call opened (409
// approval_pending, the approval under details) once the owner approved it there. An MCP client
// is refused 428 approval_required. A write without acknowledged is 409
// acknowledgment_required. Observe mode keeps no chain: 503 audit_unavailable.

func (a *API) auditRoutes() []Route {
	return []Route{
		{http.MethodPost, "/api/audit/prune/plan", ScopeAdmin, a.auditPrunePlan},
		{http.MethodPost, "/api/audit/prune", ScopeAdmin, a.auditPrune},
		{http.MethodPost, "/api/audit/reanchor/plan", ScopeAdmin, a.auditReanchorPlan},
		{http.MethodPost, "/api/audit/reanchor", ScopeAdmin, a.auditReanchor},
	}
}

// auditGate is the gate whose sink the lane writes, or false after answering that there is none.
func (a *API) auditGate(w http.ResponseWriter) (*gate.Gate, bool) {
	if a.o.Gate == nil {
		apierr.Write(w, http.StatusServiceUnavailable, string(gate.CodeAuditUnavailable),
			"this Cerberus writes no audit chain: in observe mode the old Cerberus writes it until the handover", nil)
		return nil, false
	}
	return a.o.Gate, true
}

func (a *API) auditPrunePlan(w http.ResponseWriter, r *http.Request) {
	g, ok := a.auditGate(w)
	if !ok {
		return
	}
	var args gate.AuditPruneArgs
	if err := decodeBody(r.Body, &args); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
		return
	}
	out, err := g.AuditPrunePlan(args.Before)
	if err != nil {
		writeGateErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *API) auditPrune(w http.ResponseWriter, r *http.Request) {
	g, ok := a.auditGate(w)
	if !ok {
		return
	}
	var args gate.AuditPruneArgs
	if err := decodeBody(r.Body, &args); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
		return
	}
	out, err := g.PruneAudit(GateContext(r.Context()), args)
	if err != nil {
		writeGateErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *API) auditReanchorPlan(w http.ResponseWriter, _ *http.Request) {
	g, ok := a.auditGate(w)
	if !ok {
		return
	}
	out, err := g.AuditReanchorPlan()
	if err != nil {
		writeGateErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *API) auditReanchor(w http.ResponseWriter, r *http.Request) {
	g, ok := a.auditGate(w)
	if !ok {
		return
	}
	var args gate.AuditReanchorArgs
	if err := decodeBody(r.Body, &args); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
		return
	}
	out, err := g.ReanchorAudit(GateContext(r.Context()), args)
	if err != nil {
		writeGateErr(w, err)
		return
	}
	writeJSON(w, out)
}
