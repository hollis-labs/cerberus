package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The admin lane's routes (docs/substrate-port/console-api-contract.md section 3.3): connector
// operations, the credential editor and the managed plugins, with the old bodies (internal/webui
// and the socket's /connectors and /plugins/connectors on main). Reads need cerberus.read,
// writes cerberus.admin. Every write is gated, in the contract's four forms: the base path, a
// retry with approval_id in the body, <path>/plan (the plan alone, nothing runs) and
// <path>/confirm (the body plus confirmed_plan_hash). A refusal is the flat body with the gate's
// status and code, the approval it names under details. Observe mode runs no admin lane:
// every route answers 503 admin_unavailable.
//
// Plugin install, upgrade and install review (the old build's terminal-only plugin_review.go)
// are served under the lead's ruling (a): the owner accepts, a person on the console typing the
// plugin id, or a key naming with approval_id an out-of-band approval the owner decided on the
// console (pluginReview). The old retired plugin_dir routes are not carried.

// pluginActions are the plugin lifecycle writes.
var pluginActions = []string{"load", "unload", "reload", "uninstall"}

// gatedForms are the suffixes of a gated write's forms.
var gatedForms = []string{"", "/plan", "/confirm"}

func (a *API) adminRoutes() []Route {
	rs := []Route{
		{http.MethodGet, "/api/connectors", ScopeRead, a.connectors},
		{http.MethodGet, "/api/credentials", ScopeRead, a.credentials},
		{http.MethodGet, "/api/plugins/connectors", ScopeRead, a.plugins},
		{http.MethodGet, "/api/plugins/connectors/{id}/health", ScopeRead, a.pluginHealth},
		{http.MethodPost, "/api/plugins/connectors/{id}/operations/{op}", ScopeAdmin, a.pluginOperation},
		{http.MethodGet, "/api/secrets/{service}/{key}", ScopeRead, a.secretPresence},
	}
	for _, form := range gatedForms {
		rs = append(rs,
			Route{http.MethodPost, "/api/connectors/{id}/operations/{op}" + form, ScopeAdmin, a.connectorOperation(form)},
			Route{http.MethodPost, "/api/credentials/{id}" + form, ScopeAdmin, a.credentialSave(form)},
			Route{http.MethodPost, "/api/secrets/{service}/{key}" + form, ScopeAdmin, a.secretSet(form)},
		)
		for _, action := range pluginActions {
			rs = append(rs, Route{http.MethodPost, "/api/plugins/connectors/{id}/" + action + form, ScopeAdmin, a.pluginAction(action, form)})
		}
		rs = append(rs,
			Route{http.MethodPost, "/api/plugins/connectors/install" + form, ScopeAdmin, a.pluginReview(admin.ReviewInstall, form)},
			Route{http.MethodPost, "/api/plugins/connectors/{id}/upgrade" + form, ScopeAdmin, a.pluginReview(admin.ReviewUpgrade, form)},
			Route{http.MethodPost, "/api/plugins/connectors/{id}/review" + form, ScopeAdmin, a.pluginReview(admin.ReviewMigrate, form)},
		)
	}
	return rs
}

// adminLane is the admin lane, or false after answering that there is none: observe mode,
// where the old Cerberus runs connector operations, plugins and credentials (the approvals
// routes' 503).
func (a *API) adminLane(w http.ResponseWriter) (*admin.Service, bool) {
	if a.o.Admin == nil {
		apierr.Write(w, http.StatusServiceUnavailable, "admin_unavailable",
			"this Cerberus runs no admin lane: in observe mode the old Cerberus runs connector operations, plugins and credentials until the handover", nil)
		return nil, false
	}
	return a.o.Admin, true
}

// writeScoped writes v with 200 through the request's redaction scope, as the old console and
// socket did: a credential the call resolved, a sensitive key and the regex net are removed.
func writeScoped(ctx context.Context, w http.ResponseWriter, v any) {
	data, err := redact.ScopeFrom(ctx).Marshal(v)
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, "internal", "the answer could not be encoded", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(json.RawMessage(data))
}

// writeAdminErr answers an admin-lane error: a refusal before the gate for its input with 400,
// a gate error with its status and code, anything else with 500.
func writeAdminErr(w http.ResponseWriter, err error) {
	var input admin.InputError
	if errors.As(err, &input) {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
		return
	}
	writeGateErr(w, err)
}

// confirmBody is a gated write's form fields in its body.
type confirmBody struct {
	ApprovalID        string `json:"approval_id"`
	ConfirmedPlanHash string `json:"confirmed_plan_hash"`
}

// gateOpts are the form's options: a confirm needs the hash it confirms; only the confirm form
// reads it, and a plan request spends no approval.
func (b confirmBody) gateOpts(form string) (admin.GateOpts, error) {
	opts := admin.GateOpts{ApprovalID: b.ApprovalID}
	switch form {
	case "/plan":
		opts = admin.GateOpts{Plan: true}
	case "/confirm":
		if b.ConfirmedPlanHash == "" {
			return opts, errors.New("a confirm needs confirmed_plan_hash: the hash of the plan you were shown")
		}
		opts.ConfirmedPlanHash = b.ConfirmedPlanHash
	}
	return opts, nil
}

// connectors is GET /api/connectors: every connector's definition, built-in or installed plugin.
func (a *API) connectors(w http.ResponseWriter, r *http.Request) {
	svc, ok := a.adminLane(w)
	if !ok {
		return
	}
	list := svc.Definitions()
	if list == nil {
		list = []contract.Definition{}
	}
	// Definitions are Cerberus's own schema: their declared secrets' names are names.
	writeScoped(GateContext(r.Context()), w, redact.DeclaredSchema{Value: list})
}

// connectorOperation is POST /api/connectors/{id}/operations/{op}[/plan|/confirm]: the old
// ExternalConnectorOperationArgs body.
func (a *API) connectorOperation(form string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc, ok := a.adminLane(w)
		if !ok {
			return
		}
		var body struct {
			admin.OperationArgs
			ConfirmedPlanHash string `json:"confirmed_plan_hash"`
		}
		if err := decodeBody(r.Body, &body); err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		opts, err := confirmBody{ApprovalID: body.ApprovalID, ConfirmedPlanHash: body.ConfirmedPlanHash}.gateOpts(form)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		args := body.OperationArgs
		args.Connector, args.Operation = r.PathValue("id"), r.PathValue("op")
		args.ApprovalID, args.ConfirmedPlanHash = opts.ApprovalID, opts.ConfirmedPlanHash
		args.Plan = args.Plan || opts.Plan
		if args.Config == nil {
			args.Config = map[string]any{}
		}
		ctx := GateContext(r.Context())
		out, err := svc.Execute(ctx, args)
		if err != nil {
			writeAdminErr(w, err)
			return
		}
		if form == "/plan" {
			// The plan form answers with the plan itself (ConnectorPlan).
			writeScoped(ctx, w, out.Data)
			return
		}
		writeScoped(ctx, w, out)
	}
}

// credentials is GET /api/credentials: names and presence, never a value.
func (a *API) credentials(w http.ResponseWriter, r *http.Request) {
	svc, ok := a.adminLane(w)
	if !ok {
		return
	}
	ctx := GateContext(r.Context())
	writeScoped(ctx, w, redact.DeclaredSchema{Value: svc.Credentials(ctx)})
}

// credentialSave is POST /api/credentials/{id}[/plan|/confirm]: provider_save.
func (a *API) credentialSave(form string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc, ok := a.adminLane(w)
		if !ok {
			return
		}
		var body struct {
			admin.CredentialSaveRequest
			confirmBody
		}
		if err := decodeBody(r.Body, &body); err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		opts, err := body.gateOpts(form)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		ctx := GateContext(r.Context())
		out, err := svc.SaveCredentials(ctx, r.PathValue("id"), body.CredentialSaveRequest, opts)
		if err != nil {
			writeAdminErr(w, err)
			return
		}
		if out.Plan != nil {
			writeScoped(ctx, w, out.Plan)
			return
		}
		writeScoped(ctx, w, out)
	}
}

// SecretPresence is GET /api/secrets/{service}/{key}: whether the credential store holds a value
// under that name. Never the value.
type SecretPresence struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
}

// secretPresence answers whether a stored secret has a value, so `cerberus secrets set` warns
// before it replaces one (old's check, made by the server that writes the store).
func (a *API) secretPresence(w http.ResponseWriter, r *http.Request) {
	svc, ok := a.adminLane(w)
	if !ok {
		return
	}
	name := r.PathValue("service") + "/" + r.PathValue("key")
	present, err := svc.SecretExists(r.Context(), name)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, SecretPresence{Name: name, Present: present})
}

// secretSet is POST /api/secrets/{service}/{key}[/plan|/confirm]: `cerberus secrets set`, one
// entry in the OS credential store under service "cerberus", account "<service>/<key>". Any
// well-formed name is accepted, declared by an installed connector or not (a secret backend's
// own credential is set before its plugin is installed). Body {value, acknowledged} with the
// form's fields; recorded as secrets.set with config {name}, never the value. The answer is
// SecretSetResult, or the plan for the plan form. Setting is the owner's (the lead's ruling (a)):
// a person on the console, or a key naming with approval_id the out-of-band approval its first
// call opened (409 approval_pending, the approval under details) once the owner approved it on
// the console; the approval binds the value by its keyed digest. An MCP client is refused 428
// approval_required. A write without acknowledged is 409 acknowledgment_required.
func (a *API) secretSet(form string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc, ok := a.adminLane(w)
		if !ok {
			return
		}
		var body struct {
			Value        string `json:"value"`
			Acknowledged bool   `json:"acknowledged"`
			confirmBody
		}
		if err := decodeBody(r.Body, &body); err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		opts, err := body.gateOpts(form)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		ctx := GateContext(r.Context())
		req := admin.SecretSetRequest{Name: r.PathValue("service") + "/" + r.PathValue("key"), Value: body.Value, Acknowledged: body.Acknowledged}
		out, err := svc.SetStoredSecret(ctx, req, opts)
		if err != nil {
			writeAdminErr(w, err)
			return
		}
		if out.Plan != nil {
			writeScoped(ctx, w, out.Plan)
			return
		}
		writeScoped(ctx, w, out)
	}
}

// plugins is GET /api/plugins/connectors.
func (a *API) plugins(w http.ResponseWriter, r *http.Request) {
	svc, ok := a.adminLane(w)
	if !ok {
		return
	}
	ctx := GateContext(r.Context())
	writeScoped(ctx, w, svc.Plugins().List(ctx))
}

// pluginHealth is GET /api/plugins/connectors/{id}/health.
func (a *API) pluginHealth(w http.ResponseWriter, r *http.Request) {
	svc, ok := a.adminLane(w)
	if !ok {
		return
	}
	ctx := GateContext(r.Context())
	out, err := svc.Plugins().Health(ctx, r.PathValue("id"))
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeScoped(ctx, w, out)
}

// pluginAction is POST /api/plugins/connectors/{id}/{load|unload|reload|uninstall}[/plan|/confirm].
// The body is {} or the form's fields.
func (a *API) pluginAction(action, form string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc, ok := a.adminLane(w)
		if !ok {
			return
		}
		var body confirmBody
		if err := decodeBody(r.Body, &body); err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		opts, err := body.gateOpts(form)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		do := map[string]func(context.Context, string, admin.GateOpts) (admin.PluginState, *gate.ConnectorPlan, error){
			"load": svc.Plugins().Load, "unload": svc.Plugins().Unload, "reload": svc.Plugins().Reload, "uninstall": svc.Plugins().Uninstall,
		}[action]
		ctx := GateContext(r.Context())
		st, shown, err := do(ctx, r.PathValue("id"), opts)
		if err != nil {
			writeAdminErr(w, err)
			return
		}
		if shown != nil {
			writeScoped(ctx, w, shown)
			return
		}
		writeScoped(ctx, w, st)
	}
}

// pluginOperation is POST /api/plugins/connectors/{id}/operations/{op}: a managed plugin's
// operation on the direct route, by id only.
func (a *API) pluginOperation(w http.ResponseWriter, r *http.Request) {
	svc, ok := a.adminLane(w)
	if !ok {
		return
	}
	var args admin.PluginExecArgs
	if err := decodeBody(r.Body, &args); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
		return
	}
	if args.PluginDir != "" {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", admin.PluginDirNotAccepted, nil)
		return
	}
	args.Operation = r.PathValue("op")
	ctx := GateContext(r.Context())
	out, err := svc.Plugins().Execute(ctx, r.PathValue("id"), args)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeScoped(ctx, w, out)
}

// pluginReview is POST /api/plugins/connectors/install, /api/plugins/connectors/{id}/upgrade
// and /api/plugins/connectors/{id}/review, each [/plan|/confirm]: the install review. The body
// is admin.ReviewRequest ({plugin_dir, dev, typed}; review takes no plugin_dir) with the form's
// fields. The plan form answers the review as a plan (preview kind "review") and accepts
// nothing. Accepting is the owner's: a person on the console with typed equal to the plugin
// id; or a key naming with approval_id the out-of-band approval its first call opened (409
// approval_pending, the approval under details) once the owner approved it on the console.
// An MCP client is refused 428 approval_required. A plugin already matching its review is
// 409 nothing_to_review.
func (a *API) pluginReview(kind, form string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc, ok := a.adminLane(w)
		if !ok {
			return
		}
		var body struct {
			admin.ReviewRequest
			confirmBody
		}
		if err := decodeBody(r.Body, &body); err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		opts, err := body.gateOpts(form)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", err.Error(), nil)
			return
		}
		ctx := GateContext(r.Context())
		plugins := svc.Plugins()
		var out admin.ReviewResult
		var shown *gate.ConnectorPlan
		switch kind {
		case admin.ReviewInstall:
			out, shown, err = plugins.Install(ctx, body.ReviewRequest, opts)
		case admin.ReviewUpgrade:
			out, shown, err = plugins.Upgrade(ctx, r.PathValue("id"), body.ReviewRequest, opts)
		default:
			if body.PluginDir != "" {
				apierr.Write(w, http.StatusBadRequest, "invalid_args", "a review takes no plugin_dir: it reviews the installed plugin's own directory", nil)
				return
			}
			out, shown, err = plugins.Review(ctx, r.PathValue("id"), body.ReviewRequest, opts)
		}
		switch {
		case errors.Is(err, admin.ErrNothingToReview):
			apierr.Write(w, http.StatusConflict, "nothing_to_review", redact.Text(err.Error()), nil)
		case err != nil:
			writeAdminErr(w, err)
		case shown != nil:
			writeScoped(ctx, w, shown)
		default:
			writeScoped(ctx, w, out)
		}
	}
}
