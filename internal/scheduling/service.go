package scheduling

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/libs/util/scheduler"
)

// Operations is the common vocabulary used by CLI, MCP and HTTP.
var Operations = []string{"create", "update", "delete", "get", "list", "run_now", "pause", "resume", "history", "logs", "dry_run", "register", "admin_view"}

func KnownOperation(operation string) bool {
	for _, op := range Operations {
		if operation == op {
			return true
		}
	}
	return false
}

// Call carries selectors and requested changes, never caller identity or permits.
// Revision is required for edits; RequestID deduplicates manual runs.
type Call struct {
	Registration   []Registration `json:"registration,omitempty"`
	Operation      string         `json:"operation"`
	OwnerApp       string         `json:"owner_app,omitempty"`
	ID             string         `json:"id,omitempty"`
	Job            *Job           `json:"job,omitempty"`
	Revision       string         `json:"revision,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	RequestID      string         `json:"request_id,omitempty"`
	State          string         `json:"state,omitempty"`
	FireID         string         `json:"fire_id,omitempty"`
	Limit          int            `json:"limit,omitempty"`
	After          time.Time      `json:"after,omitempty"`
	Acknowledged   bool           `json:"acknowledged,omitempty"`
	ApprovalID     string         `json:"approval_id,omitempty"`
}

type JobView struct {
	Job      Job       `json:"job"`
	Revision string    `json:"revision"`
	State    string    `json:"state"`
	NextRun  time.Time `json:"next_run"`
}

type RunView struct {
	Fire    scheduler.Fire `json:"fire"`
	Receipt *Receipt       `json:"receipt,omitempty"`
}

type Result struct {
	Job           *JobView    `json:"job,omitempty"`
	Jobs          []JobView   `json:"jobs,omitempty"`
	Runs          []RunView   `json:"runs,omitempty"`
	Run           *RunView    `json:"run,omitempty"`
	Times         []time.Time `json:"times,omitempty"`
	Deleted       bool        `json:"deleted,omitempty"`
	Logs          *RunLogs    `json:"logs,omitempty"`
	LogsAvailable bool        `json:"logs_available"`
	LogsReason    string      `json:"logs_reason,omitempty"`
}

// Error codes are stable across all adapters. Text is safe authored guidance.
type Error struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

func (e *Error) Error() string           { return e.Code + ": " + e.Message }
func Refusal(code, message string) error { return &Error{Code: code, Message: message} }
func ErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "internal"
}

// Service is the single surface contract. Adapters neither open databases nor
// start engines. A serving host explicitly supplies this dependency.
type Service interface {
	Schedule(context.Context, Call) (Result, error)
}

// Access runs the host's authenticated shared permission/audit path for every
// call, returning its outcome recorder. Nil access always refuses, even reads.
type Access func(context.Context, Call) (func(error), error)
type service struct {
	core   *Core
	access Access
}

func NewService(core *Core, access Access) Service { return &service{core: core, access: access} }
func (s *service) Schedule(ctx context.Context, req Call) (out Result, retErr error) {
	ctx, scope := redact.EnsureScope(ctx)
	defer func() {
		if retErr != nil && ErrorCode(retErr) == "internal" {
			retErr = Refusal("internal", redact.Render(scope, retErr))
		}
	}()
	if s.core == nil || s.access == nil {
		return out, Refusal("unavailable", "scheduling service is not bound by this serving host")
	}
	if err := validateCall(req); err != nil {
		return out, err
	}
	finish, err := s.access(ctx, req)
	if err != nil {
		return out, err
	}
	if finish == nil {
		return out, Refusal("forbidden", "scheduling access has no outcome recorder")
	}
	defer func() {
		if retErr == nil {
			if err := recheckAccess(ctx); err != nil {
				out = Result{}
				retErr = err
			}
		}
		if retErr != nil {
			out = Result{}
		}
		finish(retErr)
	}()
	if err := recheckAccess(ctx); err != nil {
		return out, err
	}
	c := s.core
	switch req.Operation {
	case "register":
		out.Jobs, retErr = c.register(ctx, req)
		return out, retErr
	case "create":
		v, err := c.createJob(ctx, *req.Job, req.IdempotencyKey)
		out.Job = &v
		return out, err
	case "update", "pause", "resume", "delete":
		v, err := c.editJob(ctx, req)
		if req.Operation == "delete" {
			out.Deleted = err == nil
		} else {
			out.Job = &v
		}
		return out, err
	case "get":
		v, err := c.view(ctx, req.OwnerApp, req.ID)
		out.Job = &v
		return out, err
	case "list", "admin_view":
		schedules, err := c.store.ListSchedules(ctx)
		if err != nil {
			return out, err
		}
		out.Jobs = []JobView{}
		for _, sch := range schedules {
			j, err := decodeJob(sch.Payload)
			if err != nil {
				return out, err
			}
			if req.OwnerApp != "" && j.OwnerApp != req.OwnerApp {
				continue
			}
			v, err := viewOf(j, sch)
			if err != nil {
				return out, err
			}
			if req.State == "" || v.State == req.State {
				out.Jobs = append(out.Jobs, v)
			}
		}
		return out, nil
	case "dry_run":
		j := *req.Job
		sch, err := j.schedule(c.now(), c.maxTimeout)
		if err != nil {
			return out, Refusal("invalid", err.Error())
		}
		after := req.After
		if after.IsZero() {
			after = c.now()
		}
		out.Times = []time.Time{}
		for i := 0; i < req.Limit; i++ {
			var next time.Time
			if scheduler.IsOneTime(sch) {
				if j.Timing.At.After(after) {
					next = j.Timing.At.UTC()
				}
			} else {
				next, err = scheduler.NextRunForSchedule(sch, after)
			}
			if err != nil {
				return out, Refusal("invalid", err.Error())
			}
			if next.IsZero() {
				break
			}
			out.Times = append(out.Times, next)
			after = next
		}
		return out, nil
	case "history":
		fires, err := c.History(ctx, req.OwnerApp, req.ID, req.Limit)
		if err != nil {
			return out, err
		}
		out.Runs = []RunView{}
		for _, f := range fires {
			v, err := c.runView(ctx, f)
			if err != nil {
				return out, err
			}
			out.Runs = append(out.Runs, v)
		}
		return out, nil
	case "logs":
		f, found, err := c.Fire(ctx, req.FireID)
		if err != nil {
			return out, err
		}
		if !found || f.ScheduleID != req.OwnerApp+"/"+req.ID {
			return out, Refusal("not_found", "run was not found for this job")
		}
		logs, logErr := c.Logs(ctx, req.OwnerApp, req.ID, req.FireID)
		out.Logs = &logs
		out.LogsAvailable, out.LogsReason = logs.Available, logs.Reason
		return out, logErr
	case "run_now":
		v, err := c.runNow(ctx, req)
		out.Run = &v
		return out, err
	default:
		return out, Refusal("invalid", "unknown scheduling operation")
	}
}
func validateCall(r Call) error {
	if r.Operation == "register" {
		if !namePattern.MatchString(r.OwnerApp) || !namePattern.MatchString(r.IdempotencyKey) || len(r.Registration) < 1 || len(r.Registration) > MaxRegistrationJobs {
			return Refusal("invalid", "registration requires app, idempotency key and 1..32 jobs")
		}
		seen := map[string]bool{}
		for _, entry := range r.Registration {
			if entry.Job.OwnerApp != r.OwnerApp || entry.Job.Generation != 0 || entry.Job.Incarnation != 0 || seen[entry.Job.ID] || entry.Absent == (entry.Revision != "") {
				return Refusal("invalid", "registration requires unique jobs in one app and exactly one absent or revision precondition")
			}
			seen[entry.Job.ID] = true
		}
	} else if len(r.Registration) != 0 {
		return Refusal("invalid", "registration is only accepted by register")
	}

	if !KnownOperation(r.Operation) {
		return Refusal("invalid", "unknown scheduling operation")
	}
	if r.Operation == "create" || r.Operation == "update" || r.Operation == "dry_run" {
		if r.Job == nil || (r.Job.Generation != 0 || r.Job.Incarnation != 0) {
			return Refusal("invalid", "job is required and generation is server assigned")
		}
	} else if r.Job != nil {
		return Refusal("invalid", "job is not accepted for this operation")
	}
	if r.Operation != "create" && r.Operation != "dry_run" && r.Operation != "list" && r.Operation != "admin_view" && r.Operation != "register" && (!namePattern.MatchString(r.OwnerApp) || !namePattern.MatchString(r.ID)) {
		return Refusal("invalid", "owner_app and id must be valid names")
	}
	if (r.Operation == "list" || r.Operation == "admin_view") && (r.OwnerApp != "" && !namePattern.MatchString(r.OwnerApp) || r.State != "" && r.State != "enabled" && r.State != "paused" && r.State != "completed") {
		return Refusal("invalid", "invalid app or state filter")
	}
	if r.Operation == "update" && (r.Job.OwnerApp != r.OwnerApp || r.Job.ID != r.ID) {
		return Refusal("invalid", "update cannot move a job to another selector")
	}
	if r.Operation == "update" || r.Operation == "pause" || r.Operation == "resume" || r.Operation == "delete" {
		if r.Revision == "" {
			return Refusal("invalid", "revision is required for edits")
		}
	}
	if r.Operation == "create" && !namePattern.MatchString(r.IdempotencyKey) {
		return Refusal("invalid", "create requires a names-only idempotency key")
	}
	if r.Operation == "run_now" && !namePattern.MatchString(r.RequestID) {
		return Refusal("invalid", "run_now requires a names-only request_id")
	}
	if r.Operation == "dry_run" && (r.Limit < 1 || r.Limit > 100) || r.Operation == "history" && (r.Limit < 1 || r.Limit > 1000) {
		return Refusal("invalid", "limit is outside the operation bounds")
	}
	if r.Operation == "logs" && r.FireID == "" {
		return Refusal("invalid", "fire_id is required")
	}
	return nil
}
func viewOf(j Job, s scheduler.Schedule) (JobView, error) {
	revision, err := j.Revision()
	state := "enabled"
	if !j.Enabled {
		state = "paused"
	} else if !s.Enabled {
		state = "completed"
	}
	return JobView{Job: j, Revision: revision, State: state, NextRun: s.NextRun}, err
}
func (c *Core) view(ctx context.Context, owner, id string) (JobView, error) {
	s, found, err := c.store.GetSchedule(ctx, owner+"/"+id)
	if err != nil {
		return JobView{}, err
	}
	if !found {
		return JobView{}, Refusal("not_found", "scheduled job was not found")
	}
	j, err := decodeJob(s.Payload)
	if err != nil {
		return JobView{}, err
	}
	return viewOf(j, s)
}
func (c *Core) runView(ctx context.Context, f scheduler.Fire) (RunView, error) {
	v := RunView{Fire: f}
	r, found, err := c.Receipt(ctx, f.ID)
	if found {
		v.Receipt = &r
	}
	return v, err
}
