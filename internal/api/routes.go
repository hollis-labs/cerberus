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
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	libauth "github.com/hollis-labs/libs/security/auth"
	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
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
	// Home is the home directory install layouts derive from (log paths).
	Home string
	// Mutator runs L7 and L8 (the supervision lane); nil answers them 409 observe_mode.
	Mutator Mutator
}

// Mutator runs one resource mutation. A *supervise.Refusal is answered with its status and
// the flat {code, message} body; a result, successful or not, with 200.
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
		mux.Handle(rt.Method+" "+rt.Pattern, legacyVersion(rt.Handler))
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
		// Approvals are decided by the owner's console session (slice 6 serves them). They are
		// registered now so the scope and the person check are in force from the start.
		Route{http.MethodPost, "/api/approvals/{id}/decide", ScopeApprove, personOnly(notYet("deciding an approval", "slice 6"))},
		Route{http.MethodPost, "/api/approvals/{id}/revoke", ScopeApprove, personOnly(notYet("revoking a grant", "slice 6"))},
	)
	return rs
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
	writeJSON(w, h)
}

func (a *API) resources(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := listResources(r.Context(), a.o.Resources, a.o.Observer,
		ListFilter{ProjectID: q.Get("project_id"), Connector: q.Get("connector"), Tag: q.Get("tag")})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, list)
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
	writeJSON(w, runtimeStatus(d, obs))
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
	writeJSON(w, ResourceInspect{
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
	writeJSON(w, ResourceDoctor{
		ResourceID: d.ID, Status: st.Status,
		Summary: "observe mode: this build reports state only; the full diagnosis arrives with supervision",
		Checks:  []ResourceDoctorCheck{},
	})
}

func (a *API) logs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := findResource(a.o.Resources, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	lines := 50
	if n, perr := strconv.Atoi(r.URL.Query().Get("lines")); perr == nil && n > 0 {
		lines = n
	}
	stream := r.URL.Query().Get("stream")
	sp, ok := spec(d)
	if !ok {
		writeErr(w, fmt.Errorf("%w: resource %q has no local process logs", errNotFound, id))
		return
	}
	path, err := logPath(a.o.Home, d, sp, stream)
	if err != nil {
		writeErr(w, fmt.Errorf("%w: %s", errNotFound, err.Error()))
		return
	}
	content, err := readLastNLines(path, lines)
	if os.IsNotExist(err) {
		content, err = fmt.Sprintf("Log file does not exist: %s", path), nil
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if !strings.HasPrefix(content, "Log file does not exist: ") {
		content = localconn.OutputRedactor(sp).Text(content)
	}
	writeJSON(w, LogLines{ResourceID: id, Stream: stream, LogPath: path, Content: content})
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
		out, err := a.o.Mutator.Mutate(r.Context(), req)
		if err != nil {
			var refusal *supervise.Refusal
			if errors.As(err, &refusal) {
				apierr.Write(w, refusal.Status, refusal.Code, refusal.Message, nil)
				return
			}
			writeErr(w, err)
			return
		}
		writeJSON(w, opResult(out))
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
	return out
}

func unknownAction(w http.ResponseWriter, r *http.Request) {
	apierr.Write(w, http.StatusNotFound, "not_found", fmt.Sprintf("unknown resource action %q", r.PathValue("action")), nil)
}

func notYet(what, slice string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		apierr.Write(w, http.StatusNotImplemented, "not_implemented", what+" arrives with "+slice, nil)
	}
}

// personOnly refuses any caller that is not a signed-in person in a console session: an API
// key, a token minted from one, and a local-trust principal all get 403 user_required, the
// identity module's answer for person-only routes. The guard has already checked the scope.
func personOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := libauth.FromContext(r.Context())
		if !ok || !IsConsolePerson(p) {
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
