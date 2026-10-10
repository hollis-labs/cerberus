package cerbapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/scheduling"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func ScheduleDefinition() contract.Definition {
	ops := make([]contract.Operation, 0, len(scheduling.Operations))
	for _, name := range scheduling.Operations {
		effect := contract.EffectRead
		switch name {
		case "create", "update", "pause", "resume":
			effect = contract.EffectWrite
		case "delete":
			effect = contract.EffectDestructive
		case "run_now":
			effect = contract.EffectExec
		case "history", "logs":
			effect = contract.EffectReadSensitive
		}
		ops = append(ops, contract.Operation{Name: name, Description: "Scheduled job " + name + "; execution always needs separate exact-fire authority.", Effect: effect, RequiresAck: effect == contract.EffectWrite || effect == contract.EffectDestructive || effect == contract.EffectExec, Target: contract.TargetDescriptor{Kind: "cerberus.schedule", From: []string{"job_key"}}, Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone})
	}
	return contract.Finalize(contract.Definition{ID: "schedule", Version: "builtin", ResourceTypes: []string{"schedule"}, Operations: ops})
}

// NewScheduleService binds the inactive core to existing serving identity,
// scope, audit, brakes and policy gates. Namespace strings confer no access.
func NewScheduleService(core *scheduling.Core, sink audit.Sink) scheduling.Service {
	return scheduling.NewService(core, func(ctx context.Context, r scheduling.Call) (func(error), error) {
		p, ok := PrincipalFrom(ctx)
		surface := CallerSurfaceFrom(ctx)
		bound := ok && (surface == SurfaceSocket && p.UIDVerified || surface == SurfaceWeb && p.Via == ViaWeb && p.Session != "" && !p.SelfReported || p.Verified() && (surface == SurfaceSocket || surface == SurfaceUnknown))
		if !bound {
			return nil, scheduling.Refusal("forbidden", "scheduling requires a serving host's verified caller binding; job and app names are selectors only")
		}
		if sink == nil {
			return nil, scheduling.Refusal("unavailable", "scheduling requires the shared audit sink")
		}
		op, known := ScheduleDefinition().Operation(r.Operation)
		if op.RequiresAck && !r.Acknowledged {
			return nil, scheduling.Refusal("ack_required", "acknowledge this schedule mutation; this does not authorize execution")
		}
		owner, id := r.OwnerApp, r.ID
		if r.Job != nil {
			owner, id = r.Job.OwnerApp, r.Job.ID
		}
		spec := auditSpec{connector: "schedule", operation: r.Operation, op: op, known: known, acknowledged: r.Acknowledged, approvalID: r.ApprovalID, config: map[string]any{"job_key": owner + "/" + id, "request": r}}
		spec.plan = func(planCtx context.Context) (plan.Plan, error) {
			digestRequest := r
			digestRequest.ApprovalID = ""
			current, found, err := core.Get(planCtx, owner, id)
			if err != nil {
				return plan.Plan{}, err
			}
			state := "absent"
			if found {
				state, err = current.Revision()
				if err != nil {
					return plan.Plan{}, err
				}
			}
			resolved, _ := auditTarget(spec)
			return plan.Plan{Lane: "schedule", Connector: "schedule", Operation: r.Operation, Effect: string(op.Effect), Target: resolved, ArgsDigest: sink.Digest(digestRequest), State: state}, nil
		}
		call, err := beginGated(ctx, sink, slog.Default(), spec)
		if err != nil {
			var external *ExternalConnectorError
			if errors.As(err, &external) {
				details, _ := json.Marshal(external.Approval)
				return nil, &scheduling.Error{Code: string(external.Code), Message: redact.Render(redact.ScopeFrom(ctx), err), Details: details}
			}
			return nil, err
		}
		return call.finish, nil
	})
}

func WithScheduleService(service scheduling.Service) InProcessOption {
	return func(c *InProcessClient) { c.schedules = service }
}
func (c *InProcessClient) Schedule(ctx context.Context, r scheduling.Call) (scheduling.Result, error) {
	if c.schedules == nil {
		return scheduling.Result{}, scheduling.Refusal("unavailable", "scheduling service is not bound by this serving host")
	}
	return c.schedules.Schedule(ctx, r)
}

// ScheduleClient keeps optional scheduling separate from older Client fakes.
func ScheduleClient(client any) scheduling.Service {
	if s, ok := client.(scheduling.Service); ok {
		return s
	}
	return scheduling.NewService(nil, nil)
}

type scheduleEnvelope struct {
	Result   *scheduling.Result `json:"result,omitempty"`
	Error    *scheduling.Error  `json:"error,omitempty"`
	Rendered bool               `json:"rendered"`
}

// ScheduleHTTP serves only the versioned contract routes. Its host must run
// existing authentication before this handler; absent a binding refuses.
func ScheduleHTTP(service scheduling.Service, prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeScheduleEnvelope(w, http.StatusMethodNotAllowed, nil, scheduling.Refusal("invalid", "use POST with the versioned scheduling contract"))
			return
		}
		op := strings.TrimPrefix(r.URL.Path, prefix)
		if op == r.URL.Path || strings.Contains(op, "/") {
			writeScheduleEnvelope(w, http.StatusNotFound, nil, scheduling.Refusal("invalid", "unknown scheduling route"))
			return
		}
		var req scheduling.Call
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeScheduleEnvelope(w, http.StatusBadRequest, nil, scheduling.Refusal("invalid", "invalid scheduling JSON body"))
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF || op != "call" && req.Operation != "" && req.Operation != op {
			writeScheduleEnvelope(w, http.StatusBadRequest, nil, scheduling.Refusal("invalid", "route and body must name the same single operation"))
			return
		}
		if op != "call" {
			req.Operation = op
		}
		boundService := service
		if boundService == nil {
			boundService = ScheduleClient(nil)
		}
		out, err := boundService.Schedule(r.Context(), req)
		status := http.StatusOK
		if err != nil {
			switch scheduling.ErrorCode(err) {
			case "invalid":
				status = http.StatusBadRequest
			case "not_found":
				status = http.StatusNotFound
			case "conflict":
				status = http.StatusConflict
			case "unavailable":
				status = http.StatusServiceUnavailable
			case "internal":
				status = http.StatusInternalServerError
			default:
				status = http.StatusForbidden
			}
		}
		writeScheduleEnvelope(w, status, &out, err)
	})
}
func writeScheduleEnvelope(w http.ResponseWriter, status int, out *scheduling.Result, err error) {
	envelope := scheduleEnvelope{Result: out, Rendered: true}
	if err != nil {
		envelope.Result = nil
		var coded *scheduling.Error
		if errors.As(err, &coded) {
			copyError := *coded
			envelope.Error = &copyError
		} else {
			envelope.Error = &scheduling.Error{Code: scheduling.ErrorCode(err), Message: redact.Render(ResponseScope(w), err)}
		}
	}
	writeJSON(w, status, envelope)
}
func (c *SocketClient) Schedule(ctx context.Context, r scheduling.Call) (scheduling.Result, error) {
	ok := scheduling.KnownOperation(r.Operation)
	if !ok {
		return scheduling.Result{}, scheduling.Refusal("invalid", "unknown scheduling operation")
	}
	body, err := json.Marshal(r)
	if err != nil {
		return scheduling.Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://cerberus-daemon/schedules/v1/call", bytes.NewReader(body))
	if err != nil {
		return scheduling.Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(APIHeaderName, APIVersion)
	c.setPrincipal(req)
	response, err := c.http.Do(req)
	if err != nil {
		return scheduling.Result{}, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return scheduling.Result{}, err
	}
	var envelope scheduleEnvelope
	if err = json.Unmarshal(data, &envelope); err != nil {
		var previous ErrorResponse
		if json.Unmarshal(data, &previous) == nil && previous.Error != "" {
			code := "forbidden"
			if previous.Code != "" {
				code = string(previous.Code)
			}
			return scheduling.Result{}, scheduling.Refusal(code, previous.Error)
		}
		return scheduling.Result{}, scheduling.Refusal("unavailable", "serving host does not support the scheduling contract")
	}
	if envelope.Error != nil {
		return scheduling.Result{}, envelope.Error
	}
	if response.StatusCode != http.StatusOK || envelope.Result == nil {
		return scheduling.Result{}, scheduling.Refusal("unavailable", "serving host did not return the scheduling contract")
	}
	return *envelope.Result, nil
}
