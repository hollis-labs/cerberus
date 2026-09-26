package webui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hollis-labs/cerberus/internal/audit"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/loopback"
	"github.com/hollis-labs/cerberus/internal/policy"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	secretpkg "github.com/hollis-labs/cerberus/pkg/secret"
	gowebui "github.com/hollis-labs/go-webui"
)

//go:embed all:dist
var embeddedUI embed.FS

// distFS is the sub-filesystem rooted at the embedded dist directory. We
// compute it once at package init because fs.Sub on a build-time-embedded FS
// with a constant path is a build invariant: if it fails, the binary is
// misbuilt and there's nothing to do but panic. Doing it here surfaces the
// failure at program start rather than on first HTTP request.
var distFS = mustSubDist()

func mustSubDist() fs.FS {
	sub, err := fs.Sub(embeddedUI, "dist")
	if err != nil {
		panic(fmt.Errorf("webui: subset embedded dist fs: %w", err))
	}
	return sub
}

type Server struct {
	audit      audit.Sink
	client     cerbapi.Client
	configPath string
	secrets    secretpkg.Provider
	logger     *slog.Logger
	sessions   *sessionStore
	guard      *loopback.Guard
	posture    func() policy.PostureSummary
}

// New constructs the console. The audit sink is required: operations the
// console runs itself — a deployment-profile run — are recorded to it.
func New(client cerbapi.Client, sink audit.Sink, configPath string, secrets secretpkg.Provider, logger *slog.Logger) (*Server, error) {
	if sink == nil {
		return nil, errors.New("webui: New requires an audit sink")
	}
	if logger == nil {
		logger = slog.Default()
	}
	sessions, err := newSessionStore()
	if err != nil {
		return nil, err
	}
	return &Server{client: client, audit: sink, configPath: configPath, secrets: secrets, logger: logger, sessions: sessions}, nil
}

// SetPosture tells the console where to read the applied posture it shows
// in its header. Without it the console shows secure, the default.
func (s *Server) SetPosture(current func() policy.PostureSummary) { s.posture = current }

func (s *Server) currentPosture() policy.PostureSummary {
	if s.posture == nil {
		return policy.PostureSummary{Global: policy.PostureSecure, Snapshot: policy.SnapshotBaseline}
	}
	return s.posture()
}

// SetSessionLimits overrides how long a sign-in link, an idle session and a
// session at all last. A zero leaves that limit at its default.
func (s *Server) SetSessionLimits(loginTTL, idle, maxAge time.Duration) {
	if loginTTL > 0 {
		s.sessions.loginTTL = loginTTL
	}
	if idle > 0 {
		s.sessions.idle = idle
	}
	if maxAge > 0 {
		s.sessions.max = maxAge
	}
}

// Handler returns the web UI handler: guard's Host and Origin checks first,
// then a signed-in session for every /api/ request (session.go). guard is
// required.
func (s *Server) Handler(guard *loopback.Guard) http.Handler {
	if guard == nil {
		panic("webui: Handler requires a loopback guard")
	}
	s.guard = guard
	mux := s.routes()
	return s.withLogging(guard.Middleware(toLocalhost(markWebSurface(s.requireSession(mux)))))
}

// markWebSurface begins every console request as the web surface, so a
// service it reaches in-process refuses local-only inputs just as the daemon
// would, and the request has its own redaction scope.
func markWebSurface(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(cerbapi.BeginHTTPRequest(w, r, cerbapi.SurfaceWeb))
	})
}

// route is one registered API pattern. routeTable is the single list the mux
// is built from, so a test can enumerate every route and require each one to
// be classified as read-only or guarded.
type route struct {
	pattern string
	handler http.HandlerFunc
}

func (s *Server) routeTable() []route {
	return []route{
		{"/api/session", s.handleSession},
		{"/api/logout", s.handleLogout},
		{"/api/approvals", s.handleApprovals},
		{"/api/approvals/", s.handleApprovalByID},
		{"/api/resources", s.handleResources},
		{"/api/resources/", s.handleResourceByID},
		// Full cerbapi.Client domain (CW-20260517-0039). Handlers stay thin
		// over the client; mutating routes reuse the actionToken guard.
		{"/api/overview", s.handleOverview},
		{"/api/settings", s.handleSettings},
		{"/api/system", s.handleSystem},
		{"/api/health", s.handleHealth},
		{"/api/projects", s.handleProjects},
		{"/api/pipelines", s.handlePipelines},
		{"/api/pipelines/", s.handlePipelineByID},
		{"/api/registry", s.handleRegistry},
		{"/api/registry/health", s.handleRegistryHealth},
		{"/api/registry/register", s.handleRegistryRegister},
		{"/api/registry/deregister", s.handleRegistryDeregister},
		{"/api/config/validate", s.handleConfigValidate},
		{"/api/config/resolve", s.handleConfigResolve},
		{"/api/config/migrate", s.handleConfigMigrate},
		{"/api/config/migrate/preview", s.handleConfigMigratePreview},
		{"/api/config/backups", s.handleConfigBackups},
		{"/api/config/backups/restore", s.handleConfigRestoreBackup},
		{"/api/connectors", s.handleConnectors},
		{"/api/connectors/", s.handleConnectorByID},
		{"/api/infra", s.handleInfra},
		{"/api/infra/providers/", s.handleInfraProviderByID},
		{"/api/deployments", s.handleDeployments},
		{"/api/deployments/", s.handleDeploymentByID},
		{"/api/plugins/connectors", s.handleManagedPlugins},
		{"/api/plugins/connectors/health", s.handlePluginDirRetired},
		{"/api/plugins/connectors/operations/", s.handlePluginDirRetired},
		{"/api/plugins/connectors/", s.handleManagedPluginByID},
	}
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	for _, rt := range s.routeTable() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	// Outside /api/ and so outside requireSession: this is where a session
	// starts. It spends a one-time token, which is its own guard.
	mux.HandleFunc("/login", s.handleLogin)
	mux.Handle("/", gowebui.Handler(gowebui.Config{FS: distFS, BasePath: "/"}))
	return mux
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.logger.Debug("webui.api_request", "method", r.Method, "path", r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Only a signed-in session reaches here (requireSession), and it gets
	// its own action token — the per-session half of the CSRF check on
	// every state-changing request. It is a credential by design, so it
	// stays out of the redaction path.
	sess := webSession(r.Context())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	posture := s.currentPosture()
	_ = json.NewEncoder(w).Encode(map[string]any{"action_token": sess.actionToken, "session": sess.ID,
		"posture": map[string]any{"summary": posture.String(), "permissive": posture.Permissive(), "detail": posture}})
}

func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := s.client.ListResources(r.Context(), cerbapi.ResourceListArgs{
		ProjectID: r.URL.Query().Get("project"),
		Connector: r.URL.Query().Get("connector"),
		Tag:       r.URL.Query().Get("tag"),
	})
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleResourceByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/resources/")
	if rest == "" {
		writeError(w, http.StatusBadRequest, "resource id required")
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	switch action {
	case "":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		out, err := s.client.GetResourceRuntime(r.Context(), id)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "logs":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		lines := 100
		if raw := r.URL.Query().Get("lines"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				lines = n
			}
		}
		stream := r.URL.Query().Get("stream")
		out, err := s.client.ResourceLogs(r.Context(), id, lines, stream)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "inspect":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		out, err := s.client.GetResourceInspect(r.Context(), id)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "doctor":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		out, err := s.client.GetResourceDoctor(r.Context(), id)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "apply", "deploy", "reload", "stop", "sync", "remove":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !s.allowStateChangingRequest(r) {
			writeError(w, http.StatusForbidden, "state-changing request rejected")
			return
		}
		opts, err := decodeMutationBody(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := s.performAction(r.Context(), id, action, opts...)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown resource action %q", action))
	}
}

func (s *Server) allowStateChangingRequest(r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return false
	}
	// The session cookie says who; the session's own token, which only a
	// page that read /api/session as that session can send, says the
	// request came from the console and not from another page the browser
	// holds the cookie for.
	sess := webSession(r.Context())
	if sess == nil || !sameToken(r.Header.Get("X-Cerberus-Web-Token"), sess.actionToken) {
		return false
	}
	// Compare against the guard's allowed set, never against r.Host: under
	// DNS rebinding the attacker controls Host and Origin together.
	if s.guard == nil || !s.guard.HostAllowed(r.Host) {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return s.guard.OriginAllowed(origin)
}

// mutationBody is what the console sends with a resource action or a
// pipeline run: the operator's acknowledgment, given in the confirm step.
type mutationBody struct {
	Acknowledged bool `json:"acknowledged"`
}

func decodeMutationBody(r *http.Request) ([]cerbapi.MutationOption, error) {
	var body mutationBody
	if err := decodeJSONBody(r, &body); err != nil {
		return nil, err
	}
	return []cerbapi.MutationOption{cerbapi.WithAcknowledged(body.Acknowledged)}, nil
}

func (s *Server) performAction(ctx context.Context, id, action string, opts ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	switch action {
	case "apply":
		return s.client.ApplyResource(ctx, id, opts...)
	case "deploy":
		return s.client.DeployResource(ctx, id, opts...)
	case "reload":
		return s.client.ReloadResource(ctx, id, opts...)
	case "stop":
		return s.client.StopResource(ctx, id, opts...)
	case "sync":
		return s.client.SyncResource(ctx, id, opts...)
	case "remove":
		return s.client.RemoveResource(ctx, id, opts...)
	default:
		return nil, fmt.Errorf("unsupported action %q", action)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	data, err := cerbapi.ResponseScope(w).Marshal(v)
	if err == nil {
		_ = json.NewEncoder(w).Encode(json.RawMessage(data))
	}
}

// writeError is the console's error body. The console's API client renders
// `message`; `error` stays for callers that read the daemon's shape.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeErrorBody(w, status, msg, "")
}

func writeErrorBody(w http.ResponseWriter, status int, msg string, code cerbapi.ExternalConnectorErrorCode) {
	body := map[string]any{
		"success": false,
		"error":   msg,
		"message": msg,
	}
	if code != "" {
		body["code"] = code
	}
	writeJSON(w, status, body)
}

// writeClientError maps a service error to a status. A connector refusal
// reads its status from the table the socket server uses too, so the console
// answers a refusal the way the daemon does rather than with a 500.
func writeClientError(w http.ResponseWriter, err error) {
	var connErr *cerbapi.ExternalConnectorError
	switch {
	case isDaemonUnavailable(err):
		writeError(w, http.StatusServiceUnavailable, "cerberus daemon unavailable or not responding; check 'cerberus daemon status'")
	case isTimeoutError(err):
		writeError(w, http.StatusServiceUnavailable, "cerberus daemon timed out while gathering resource state; check 'cerberus daemon status'")
	case errors.As(err, &connErr):
		writeErrorBody(w, cerbapi.ExternalConnectorHTTPStatus(err, http.StatusInternalServerError), err.Error(), connErr.Code)
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func isDaemonUnavailable(err error) bool {
	var unreachable *cerbapi.DaemonUnreachableError
	return errors.As(err, &unreachable)
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// decodeJSONBody decodes a request JSON body into dst. An empty body is
// treated as a zero value, matching the socket server's contract.
func decodeJSONBody(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()                                   //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB cap
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

// ---- domain handlers (CW-20260517-0039) ----
//
// Route map exposed to the Sysop UI (CW-20260515-0139):
//
//	GET  /api/health                              ?resource=<id> optional
//	GET  /api/projects
//	GET  /api/resources/{id}/inspect
//	GET  /api/resources/{id}/doctor
//	POST /api/resources/{id}/sync                  guarded
//	POST /api/resources/{id}/remove                guarded
//	GET  /api/pipelines
//	POST /api/pipelines/{id}/run                   guarded
//	GET  /api/connectors
//	POST /api/connectors/{id}/operations/{op}      guarded
//	GET  /api/plugins/connectors
//	POST /api/plugins/connectors/health            retired (410): took a plugin_dir
//	POST /api/plugins/connectors/operations/{op}   retired (410): took a plugin_dir
//	POST /api/plugins/connectors/install           retired (410): install from the CLI
//	POST /api/plugins/connectors/{id}/load         guarded
//	POST /api/plugins/connectors/{id}/unload       guarded
//	GET  /api/plugins/connectors/{id}/health
//	POST /api/plugins/connectors/{id}/operations/{op}  guarded
//
// "guarded" routes require the actionToken via allowStateChangingRequest.

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	out, err := s.client.Health(r.Context(), r.URL.Query().Get("resource"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := s.client.ListProjects(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	if list == nil {
		list = []cerbapi.ProjectInfo{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handlePipelines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := s.client.ListPipelines(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	if list == nil {
		list = []cerbapi.PipelineInfo{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handlePipelineByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/pipelines/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	if id == "" || action != "run" {
		writeError(w, http.StatusNotFound, "expected POST /api/pipelines/{id}/run")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.allowStateChangingRequest(r) {
		writeError(w, http.StatusForbidden, "state-changing request rejected")
		return
	}
	opts, err := decodeMutationBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.client.RunPipeline(r.Context(), id, opts...)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleConnectors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := s.client.ListConnectors(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	if list == nil {
		list = []contract.Definition{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleConnectorByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/connectors/")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "operations" || parts[2] == "" {
		writeError(w, http.StatusNotFound, "expected POST /api/connectors/{id}/operations/{operation}")
		return
	}
	if !s.allowStateChangingRequest(r) {
		writeError(w, http.StatusForbidden, "state-changing request rejected")
		return
	}
	var args cerbapi.ExternalConnectorOperationArgs
	if err := decodeJSONBody(r, &args); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	args.Connector = parts[0]
	args.Operation = parts[2]
	if args.Config == nil {
		args.Config = map[string]any{}
	}
	out, err := s.client.ExecuteConnectorOperation(r.Context(), args)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleManagedPlugins(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := s.client.ListManagedPlugins(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	if list == nil {
		list = []cerbapi.ManagedPluginConnectorState{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handlePluginDirRetired answers the retired routes that took a plugin_dir:
// /api/plugins/connectors/health previewed a directory by installing, loading
// and running its entrypoint, and /api/plugins/connectors/operations/ ran an
// operation from one. Neither is available from a browser.
func (s *Server) handlePluginDirRetired(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusGone, cerbapi.PluginDirRetired)
}

// webPluginInstallRetired replaces install-by-path. Registering a directory
// from the browser would let anything driving the console choose code for the
// daemon to run; the operator installs from their shell and loads it here.
const webPluginInstallRetired = "installing a plugin by path is not available from the web console; run `cerberus connectors plugin managed install <dir>` in your shell, then load it here by id"

func (s *Server) handleManagedPluginByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/plugins/connectors/")
	if rest == "" {
		writeError(w, http.StatusNotFound, "expected /api/plugins/connectors/{id}/{action}")
		return
	}
	parts := strings.Split(rest, "/")

	// install: POST /api/plugins/connectors/install (no resource id) is retired.
	if len(parts) == 1 && parts[0] == "install" {
		writeError(w, http.StatusGone, webPluginInstallRetired)
		return
	}

	if len(parts) < 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "expected /api/plugins/connectors/{id}/{action}")
		return
	}
	id := parts[0]
	action := parts[1]

	switch action {
	case "health":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		out, err := s.client.ManagedPluginHealth(r.Context(), id)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "load", "unload":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !s.allowStateChangingRequest(r) {
			writeError(w, http.StatusForbidden, "state-changing request rejected")
			return
		}
		var (
			out cerbapi.ManagedPluginConnectorState
			err error
		)
		if action == "load" {
			out, err = s.client.LoadManagedPlugin(r.Context(), id)
		} else {
			out, err = s.client.UnloadManagedPlugin(r.Context(), id)
		}
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "operations":
		if len(parts) < 3 || parts[2] == "" {
			writeError(w, http.StatusNotFound, "expected POST /api/plugins/connectors/{id}/operations/{operation}")
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !s.allowStateChangingRequest(r) {
			writeError(w, http.StatusForbidden, "state-changing request rejected")
			return
		}
		var args cerbapi.PluginConnectorExecArgs
		if err := decodeJSONBody(r, &args); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if args.PluginDir != "" {
			writeError(w, http.StatusBadRequest, cerbapi.PluginDirNotAccepted)
			return
		}
		args.Operation = parts[2]
		out, err := s.client.ExecuteManagedPlugin(r.Context(), id, args)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown managed plugin action %q", action))
	}
}
