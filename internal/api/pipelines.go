package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/supervise"
)

// The old socket's pipeline routes (cerbapi socket_server.go handlePipelines and
// handlePipelinesID) under /api: the list and a definition need cerberus.read, a run
// cerberus.operate. A run takes the resource routes' body (acknowledged, approval_id, plan);
// observe mode refuses it 409 observe_mode.

// Pipelines is the supervision lane's pipeline runner.
type Pipelines interface {
	ListPipelines() ([]supervise.PipelineInfo, error)
	GetPipeline(id string) (*supervise.PipelineDetail, error)
	RunPipeline(ctx context.Context, req supervise.PipelineRequest) (*supervise.PipelineRunResult, error)
}

func (a *API) pipelinesOrUnavailable(w http.ResponseWriter) bool {
	if a.o.Pipelines == nil {
		apierr.Write(w, http.StatusServiceUnavailable, "pipelines_unavailable", "this Cerberus serves no pipeline runner", nil)
		return false
	}
	return true
}

func (a *API) pipelineList(w http.ResponseWriter, r *http.Request) {
	if !a.pipelinesOrUnavailable(w) {
		return
	}
	list, err := a.o.Pipelines.ListPipelines()
	if err != nil {
		writeErr(w, err)
		return
	}
	if list == nil {
		list = []supervise.PipelineInfo{}
	}
	writeScoped(GateContext(r.Context()), w, list)
}

// pipelineGet answers the definition, or null for an unknown id, as the old socket did.
func (a *API) pipelineGet(w http.ResponseWriter, r *http.Request) {
	if !a.pipelinesOrUnavailable(w) {
		return
	}
	out, err := a.o.Pipelines.GetPipeline(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeScoped(GateContext(r.Context()), w, out)
}

func (a *API) pipelineRun(w http.ResponseWriter, r *http.Request) {
	if !a.pipelinesOrUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	m, err := decodeMutation(r.Body, "run", id)
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
		return
	}
	ctx := GateContext(r.Context())
	out, err := a.o.Pipelines.RunPipeline(ctx, supervise.PipelineRequest{ID: id, Acknowledged: m.Acknowledged, ApprovalID: m.ApprovalID, Plan: m.Plan})
	if err != nil {
		var refusal *supervise.Refusal
		if errors.As(err, &refusal) {
			apierr.Write(w, refusal.Status, refusal.Code, refusal.Message, nil)
			return
		}
		writeGateErr(w, err)
		return
	}
	writeScoped(ctx, w, out)
}

func (a *API) pipelineRoutes() []Route {
	return []Route{
		{http.MethodGet, "/api/pipelines", ScopeRead, a.pipelineList},
		{http.MethodGet, "/api/pipelines/{id}", ScopeRead, a.pipelineGet},
		{http.MethodPost, "/api/pipelines/{id}/run", ScopeOperate, a.pipelineRun},
	}
}
