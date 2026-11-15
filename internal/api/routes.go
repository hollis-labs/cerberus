// Package api is the Substrate build's HTTP API. Every route is in Routes with the scope the
// auth guard enforces (docs/substrate-port-plan.md section 4.3), so a handler cannot be served
// without a scope: the mux and the scope table are built from the same list, and Validate
// refuses a route whose scope is not one of Scopes.
//
// The legacy-route set (docs/substrate-port/legacy-routes.md) keeps the old socket API's paths,
// queries and bodies under /api for tachyon and tether sysop.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	libauth "github.com/hollis-labs/libs/security/auth"
	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/hostmon"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// The scopes (plan section 4.3). The owner holds "*".
const (
	ScopeRead    = "cerberus.read"
	ScopeOperate = "cerberus.operate"
	ScopeAdmin   = "cerberus.admin"
	// ScopeApprove is held only by the owner's console identity session (decision 0.1). The
	// server refuses it on every API key, and the approve routes also refuse any caller that is
	// not a signed-in person.
	ScopeApprove = "cerberus.approve"
)

// Scopes are the scopes a route may require.
var Scopes = []string{ScopeRead, ScopeOperate, ScopeAdmin, ScopeApprove}

// Route is one served method and pattern (net/http ServeMux syntax, "{id}" for a segment) with
// the scope the guard requires and the handler that serves it.
type Route struct {
	Method  string
	Pattern string
	Scope   string
	Handler http.HandlerFunc
}

// Validate refuses a route list in which any route lacks a known scope, lies outside /api/, or
// repeats a method and pattern. New calls it, so a route added without a scope stops the start.
func Validate(routes []Route) error {
	seen := map[string]bool{}
	for _, rt := range routes {
		key := rt.Method + " " + rt.Pattern
		known := false
		for _, s := range Scopes {
			known = known || rt.Scope == s
		}
		switch {
		case !known:
			return fmt.Errorf("route %s has scope %q, want one of %s", key, rt.Scope, strings.Join(Scopes, ", "))
		case !strings.HasPrefix(rt.Pattern, "/api/"):
			return fmt.Errorf("route %s is outside /api/, where the guard does not look", key)
		case rt.Handler == nil:
			return fmt.Errorf("route %s has no handler", key)
		case seen[key]:
			return fmt.Errorf("route %s is listed twice", key)
		}
		seen[key] = true
	}
	return nil
}

// Options build an API.
type Options struct {
	// Resources lists the registry's resources, read fresh.
	Resources Resources
	// Observer reports runtime state; nil is NoObserver.
	Observer Observer
	// Hosts reads the declared hosts and their last known state; nil serves an empty list.
	Hosts HostSource
	// HostReads answers the host reads beyond L9 (status, samples, the monitor, events, probe)
	// and puts the monitor's status on host views; nil answers them 503 hosts_unavailable.
	HostReads *hostmon.Reads
	// Projects lists the registry's projects, read fresh; nil serves an empty list.
	Projects func() ([]config.ProjectDef, error)
	// Home is the home directory install layouts derive from (log paths).
	Home string
	// Mutator runs L7 and L8 (the supervision lane); nil answers them 409 observe_mode.
	Mutator Mutator
	// Gate is the operation gate, in supervise mode: L6 passes it as read_sensitive, and the
	// approvals routes serve its broker. Nil is observe mode, where nothing is recorded: L6 is a
	// redacted read, logged, and the approvals routes answer 503 approvals_unavailable.
	Gate *gate.Gate
	// Admin is the admin lane (connectors, credentials, plugins), in supervise mode; nil is
	// observe mode, where its routes answer 503 admin_unavailable.
	Admin *admin.Service
	// Logger receives what the API logs; nil is slog.Default.
	Logger *slog.Logger
}

// Mutator runs one resource mutation. A *supervise.Refusal is answered with its status and
// the flat {code, message} body, a *gate.Error with the old status for its code and the
// approval it names under details; a result, successful or not, with 200.
type Mutator interface {
	Mutate(ctx context.Context, req supervise.Request) (*supervise.OpResult, error)
}

// API serves the routes.
type API struct {
	o      Options
	routes []Route
}

// New builds the API and validates its routes.
func New(o Options) (*API, error) {
	if o.Resources == nil {
		return nil, errors.New("api: Resources is required")
	}
	if o.Observer == nil {
		o.Observer = NoObserver{}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	a := &API{o: o}
	a.routes = a.table()
	if err := Validate(a.routes); err != nil {
		return nil, err
	}
	return a, nil
}

// Routes is the route list the mux and the scope table are built from.
func (a *API) Routes() []Route { return append([]Route(nil), a.routes...) }

// RegisterScopes adds every route to the guard's scope table. Specific patterns come before
// the catch-all action routes, as the table matches the first route that fits.
func (a *API) RegisterScopes(t *libauth.ScopeTable) {
	for _, rt := range a.routes {
		t.Route(rt.Method, rt.Pattern, rt.Scope)
	}
}

// Mux serves the routes, behind the legacy X-Cerberus-Api check.
func (a *API) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	for _, rt := range a.routes {
		mux.Handle(rt.Method+" "+rt.Pattern, legacyVersion(stampMCPStdio(rt.Handler)))
	}
	return mux
}

// mutationVerbs are the resource verbs of the legacy set; remove needs cerberus.admin.
var mutationVerbs = []string{"reload", "stop", "deploy", "apply", "sync"}

func (a *API) table() []Route {
	rs := []Route{
		{http.MethodGet, "/api/health", ScopeRead, a.health},
		{http.MethodGet, "/api/resources", ScopeRead, a.resources},
		{http.MethodGet, "/api/resources/{id}/status", ScopeRead, a.status},
		{http.MethodGet, "/api/resources/{id}/inspect", ScopeRead, a.inspect},
		{http.MethodGet, "/api/resources/{id}/doctor", ScopeRead, a.doctor},
		{http.MethodGet, "/api/resources/{id}/logs", ScopeRead, a.logs},
	}
	for _, v := range mutationVerbs {
		rs = append(rs, Route{http.MethodPost, "/api/resources/{id}/" + v, ScopeOperate, a.mutate(v)})
	}
	rs = append(rs,
		Route{http.MethodPost, "/api/resources/{id}/remove", ScopeAdmin, a.mutate("remove")},
		// Any other action under a resource is the old 404, for a caller allowed to read.
		Route{http.MethodGet, "/api/resources/{id}/{action}", ScopeRead, unknownAction},
		Route{http.MethodPost, "/api/resources/{id}/{action}", ScopeOperate, unknownAction},
		Route{http.MethodGet, "/api/hosts", ScopeRead, a.hosts},
		// The old socket's host reads (cerbapi host_http.go, host_monitor_http.go, host_events.go).
		Route{http.MethodGet, "/api/hosts/_status", ScopeRead, a.hostStatusAll},
		Route{http.MethodGet, "/api/hosts/_monitor", ScopeRead, a.hostMonitor},
		Route{http.MethodGet, "/api/hosts/_events", ScopeRead, a.hostEvents},
		Route{http.MethodGet, "/api/hosts/{name}", ScopeRead, a.hostGet},
		Route{http.MethodPost, "/api/hosts/{name}/probe", ScopeRead, a.hostProbe},
		Route{http.MethodGet, "/api/hosts/{name}/status", ScopeRead, a.hostStatus},
		Route{http.MethodGet, "/api/hosts/{name}/samples", ScopeRead, a.hostSamples},
		Route{http.MethodGet, "/api/projects", ScopeRead, a.projects},
		// Approvals (the old socket's /approvals routes): anyone who may read sees them; only the
		// owner signed in to the console decides, revokes or acknowledges one (decision 0.1).
		Route{http.MethodGet, "/api/approvals", ScopeRead, a.approvalList},
		Route{http.MethodGet, "/api/approvals/{id}", ScopeRead, a.approvalGet},
		Route{http.MethodPost, "/api/approvals/{id}/decide", ScopeApprove, personOnly(a.approvalDecide)},
		Route{http.MethodPost, "/api/approvals/{id}/revoke", ScopeApprove, personOnly(a.approvalRevoke)},
		Route{http.MethodPost, "/api/approvals/{id}/ack-break-glass", ScopeApprove, personOnly(a.approvalAckBreakGlass)},
		// The brakes (the old socket's /brakes routes): anyone who may read sees them; engaging,
		// lifting and resetting need cerberus.admin, and the gate decides the rest as the old
		// daemon did (brakes.go): engaging is open, a lift needs a person and the owner's console
		// approval, a reset a person and the typed phrase.
		Route{http.MethodGet, "/api/brakes", ScopeRead, a.brakesGet},
		Route{http.MethodPost, "/api/brakes/lockdown", ScopeAdmin, a.brakeLockdown},
		Route{http.MethodPost, "/api/brakes/lockdown/lift", ScopeAdmin, a.brakeLiftLockdown},
		Route{http.MethodPost, "/api/brakes/freeze", ScopeAdmin, a.brakeFreeze},
		Route{http.MethodPost, "/api/brakes/freeze/{id}/lift", ScopeAdmin, a.brakeLiftFreeze},
		Route{http.MethodPost, "/api/brakes/suspensions/{id}/reset", ScopeAdmin, a.brakeResetSuspension},
	)
	rs = append(rs, a.auditRoutes()...)
	return append(rs, a.adminRoutes()...)
}

// errNotFound marks a missing resource or host.
var errNotFound = errors.New("not found")

// legacyVersion is the old socket's version rule: a missing X-Cerberus-Api or "v1" is
// accepted, anything else refused, and the answer always names v1.
func legacyVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Cerberus-Api", "v1")
		if got := r.Header.Get("X-Cerberus-Api"); got != "" && got != "v1" {
			apierr.Write(w, http.StatusBadRequest, "unsupported_api_version",
				fmt.Sprintf("unsupported X-Cerberus-Api: %s (server expects v1)", got), nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON writes v with 200, compactly, as the old socket did (json.Encoder: HTML-escaped, newline).
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		apierr.Write(w, http.StatusNotFound, "not_found", err.Error(), nil)
	case errors.Is(err, errBadArgs):
		apierr.Write(w, http.StatusBadRequest, "invalid_args", strings.TrimPrefix(err.Error(), errBadArgs.Error()+": "), nil)
	default:
		apierr.Write(w, http.StatusInternalServerError, "internal", err.Error(), nil)
	}
}

var errBadArgs = errors.New("invalid arguments")

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	defs, err := a.o.Resources()
	if err != nil {
		writeErr(w, err)
		return
	}
	want := r.URL.Query().Get("resource_id")
	h := DaemonHealth{DaemonRunning: true}
	for _, d := range defs {
		if !supervisedLocally(d.Type, d.Connector) || (want != "" && d.ID != want) {
			continue
		}
		obs := a.o.Observer.Observe(r.Context(), d)
		h.Resources = append(h.Resources, ResourceHealth{
			ResourceID: d.ID, Status: obs.State, Healthy: obs.State == "running",
			Mode: resourceMode(d), Supervisor: resourceSupervisor(d), RunFrom: resourceRunFrom(d),
		})
	}
	writeScoped(GateContext(r.Context()), w, h)
}

func (a *API) resources(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := listResources(r.Context(), a.o.Resources, a.o.Observer,
		ListFilter{ProjectID: q.Get("project_id"), Connector: q.Get("connector"), Tag: q.Get("tag")})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeScoped(GateContext(r.Context()), w, list)
}

func (a *API) observeOne(ctx context.Context, id string) (config.ResourceDef, Observation, error) {
	d, err := findResource(a.o.Resources, id)
	if err != nil {
		return d, Observation{}, err
	}
	var obs Observation
	if supervisedLocally(d.Type, d.Connector) {
		obs = a.o.Observer.Observe(ctx, d)
	}
	return d, obs, nil
}

func (a *API) status(w http.ResponseWriter, r *http.Request) {
	d, obs, err := a.observeOne(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeScoped(GateContext(r.Context()), w, runtimeStatus(d, obs))
}

// inspect answers with the status fields the observation has. The resolved command, paths
// and the raw unit text arrive with supervision (slice 5).
func (a *API) inspect(w http.ResponseWriter, r *http.Request) {
	d, obs, err := a.observeOne(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	st := runtimeStatus(d, obs)
	writeScoped(GateContext(r.Context()), w, ResourceInspect{
		ID: st.ID, Name: st.Name, Type: st.Type, Project: st.Project, Connector: st.Connector,
		Mode: st.Mode, Supervisor: st.Supervisor, RunFrom: st.RunFrom, URL: st.URL, Port: st.Port,
		Status: st.Status, ServiceName: st.ServiceName,
		RecommendedReason: st.RecommendedReason, RecommendedNextStep: st.RecommendedNextStep,
	})
}

func (a *API) doctor(w http.ResponseWriter, r *http.Request) {
	d, obs, err := a.observeOne(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	st := runtimeStatus(d, obs)
	writeScoped(GateContext(r.Context()), w, ResourceDoctor{
		ResourceID: d.ID, Status: st.Status,
		Summary: "observe mode: this build reports state only; the full diagnosis arrives with supervision",
		Checks:  []ResourceDoctorCheck{},
	})
}

// logs serves L6. Logs are text the workload wrote, so in supervise mode the read is
// read_sensitive and passes the gate like every operation, as the old build's ResourceLogs did
// (I1, CERB-GAP-889): an intent and an outcome are recorded, the brakes and policy decide it,
// an approval named by ?approval_id= is spent on it, and egress policy shapes the content. In
// observe mode no audit chain is open and nothing may be written, so the read is the redacted
// read of slice 4a, logged.
func (a *API) logs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	lines := 50
	if n, perr := strconv.Atoi(r.URL.Query().Get("lines")); perr == nil && n > 0 {
		lines = n
	}
	stream := r.URL.Query().Get("stream")
	if a.o.Gate == nil {
		a.o.Logger.Info("api.logs.read", "resource", id, "gated", false, "reason", "observe mode: no audit chain is open, so the read is redacted and not recorded")
		out, err := a.readLogs(id, lines, stream)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, out)
		return
	}
	ctx := GateContext(r.Context())
	op, known := localconn.Definition().Operation(localconn.OpLogs)
	args := map[string]any{localconn.InputID: id, "lines": lines}
	if stream != "" {
		args["stream"] = stream
	}
	call, err := a.o.Gate.Begin(ctx, gate.Spec{
		Connector: localconn.Definition().ID, Operation: localconn.OpLogs, Op: op, Known: known,
		Config: args, Resources: a.resourceLookup(), ApprovalID: r.URL.Query().Get("approval_id"),
	})
	if err != nil {
		writeGateErr(w, err)
		return
	}
	out, err := a.readLogs(id, lines, stream)
	if err == nil {
		out, err = gate.ShapeAs(call, out)
	}
	call.Finish(err)
	if err != nil {
		writeGateErr(w, err)
		return
	}
	writeJSON(w, out)
}

// resourceLookup looks resources up in one read of the registry, for the gate's target labels.
func (a *API) resourceLookup() gate.ResourceLookup {
	defs, err := a.o.Resources()
	if err != nil {
		return nil
	}
	return gate.ConfigResourceLookup(&config.ConfigV2{Resources: defs})
}

// readLogs is the old readResourceLogs: the last lines of the resource's log, redacted through
// the resource's own redactor.
func (a *API) readLogs(id string, lines int, stream string) (*LogLines, error) {
	d, err := findResource(a.o.Resources, id)
	if err != nil {
		return nil, err
	}
	sp, ok := spec(d)
	if !ok {
		return nil, fmt.Errorf("%w: resource %q has no local process logs", errNotFound, id)
	}
	path, err := logPath(a.o.Home, d, sp, stream)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errNotFound, err.Error())
	}
	content, err := readLastNLines(path, lines)
	if os.IsNotExist(err) {
		content, err = fmt.Sprintf("Log file does not exist: %s", path), nil
	}
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(content, "Log file does not exist: ") {
		content = localconn.OutputRedactor(sp).Text(content)
	}
	return &LogLines{ResourceID: id, Stream: stream, LogPath: path, Content: content}, nil
}

// writeGateErr answers a gate refusal with its old status and code, and the approval it names
// under details (legacy-routes.md section 3), and anything else as writeErr does.
func writeGateErr(w http.ResponseWriter, err error) {
	status, code, message, details, ok := gate.HTTPError(err)
	if !ok {
		writeErr(w, err)
		return
	}
	var body any
	if details.Approval != nil {
		body = map[string]any{"approval": details.Approval}
	}
	apierr.Write(w, status, string(code), message, body)
}

// logPath is the old resourceLogPath: a dev session's log, or the install root's logs/.
func logPath(home string, d config.ResourceDef, sp localconn.ProcessSpec, stream string) (string, error) {
	res := resourceToDomain(d)
	switch sp.Mode {
	case "", localconn.ProcessModeDevSession:
		return localconn.DevSessionLogPath(res.ID, sp), nil
	case localconn.ProcessModeOSService:
		layout, err := localconn.DefaultInstallLayout(home, res, sp)
		if err != nil {
			return "", err
		}
		name := "stdout.log"
		if strings.EqualFold(stream, "stderr") {
			name = "stderr.log"
		}
		return filepath.Join(layout.RootDir, "logs", name), nil
	}
	return "", fmt.Errorf("unsupported process mode %q for resource %q", sp.Mode, d.ID)
}

// mutate serves L7 and L8 (docs/substrate-port/legacy-routes.md) as the old socket did: the
// body is JSON or empty (an empty body carries no acknowledgment), at most 1 MiB, unknown
// fields ignored. The lane answers a refusal before the run with its status and code, and
// every run, failed or not, with 200 and the OpResult.
func (a *API) mutate(verb string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if a.o.Mutator == nil {
			apierr.Write(w, http.StatusConflict, "observe_mode", fmt.Sprintf(
				"this Cerberus is in observe mode, so it refused %s of %q: the old Cerberus still supervises the workloads and runs every change", verb, id), nil)
			return
		}
		req, err := decodeMutation(r.Body, verb, id)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		// One request scope for the mutation and its answer, so what the call put in scope is
		// redacted from what it returns, as main's socket answered.
		ctx := GateContext(r.Context())
		out, err := a.o.Mutator.Mutate(ctx, req)
		if err != nil {
			var refusal *supervise.Refusal
			if errors.As(err, &refusal) {
				apierr.Write(w, refusal.Status, refusal.Code, refusal.Message, nil)
				return
			}
			writeGateErr(w, err)
			return
		}
		writeScoped(ctx, w, opResult(out))
	}
}

// decodeMutation is the old socket's decodeMutationOptions for the plain route: a
// confirmed_plan_hash in the body is ignored there, since only a confirm route sets it.
func decodeMutation(body io.Reader, verb, id string) (supervise.Request, error) {
	req := supervise.Request{Verb: verb, ID: id}
	var data []byte
	if body != nil {
		var err error
		if data, err = io.ReadAll(io.LimitReader(body, 1<<20)); err != nil {
			return req, fmt.Errorf("read mutation body: %w", err)
		}
	}
	if len(data) == 0 {
		return req, nil
	}
	var opts struct {
		Acknowledged              bool   `json:"acknowledged"`
		InstallAfterBuildOverride *bool  `json:"install_after_build_override"`
		ApprovalID                string `json:"approval_id"`
		Plan                      bool   `json:"plan"`
	}
	if err := json.Unmarshal(data, &opts); err != nil {
		return req, fmt.Errorf("decode mutation body: %w", err)
	}
	req.Acknowledged, req.InstallAfterBuildOverride, req.ApprovalID, req.Plan =
		opts.Acknowledged, opts.InstallAfterBuildOverride, opts.ApprovalID, opts.Plan
	return req, nil
}

// opResult is the lane's result in the API's OpResult.
func opResult(o *supervise.OpResult) OpResult {
	out := OpResult{
		Warnings:       o.Warnings,
		BuildPerformed: o.BuildPerformed,
		Success:        o.Success,
		ServiceID:      o.ServiceID,
		Message:        o.Message,
		BuildOutput:    o.BuildOutput,
		BuildLogPath:   o.BuildLogPath,
		InstallOutput:  o.InstallOutput,
		InstallSkipped: o.InstallSkipped,
		Error:          o.Error,
	}
	if o.Activation != nil {
		out.Activation = &ActivationArtifact{Path: o.Activation.Path, SHA256: o.Activation.SHA256, ModifiedAt: o.Activation.ModifiedAt}
	}
	if o.Plan != nil {
		out.Plan, _ = json.Marshal(o.Plan)
	}
	return out
}

func unknownAction(w http.ResponseWriter, r *http.Request) {
	apierr.Write(w, http.StatusNotFound, "not_found", fmt.Sprintf("unknown resource action %q", r.PathValue("action")), nil)
}

// personOnly refuses any caller that is not a signed-in person in a console session: an API
// key, a token minted from one, and a local-trust principal all get 403 user_required, the
// identity module's answer for person-only routes. The guard has already checked the scope.
func personOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := libauth.FromContext(r.Context())
		_, viaMCP := mcpCallerFrom(r.Context())
		// An MCP tool call is an agent's whoever sent it, even a console session on /mcp.
		if !ok || !IsConsolePerson(p) || viaMCP {
			apierr.Write(w, http.StatusForbidden, "user_required", "this needs the owner signed in to the console", nil)
			return
		}
		next(w, r)
	}
}

// IsConsolePerson reports whether p is a signed-in person in a console session, the only kind of
// caller allowed to approve: the identity module's session principal (kind "user", a session
// id), never a token minted from it at /api/token (which carries a token id or a parent, even
// when it still names the person), an API key, or local trust.
func IsConsolePerson(p libauth.Principal) bool {
	return p.Verified && p.Kind == "user" && strings.HasPrefix(p.ID, "user:") && p.SessionID != "" &&
		p.TokenID == "" && p.Parent == ""
}
