package cerbapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/redact"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	gmcp "github.com/hollis-labs/go-mcp/server"
)

// SocketServer exposes a Client over a unix-socket HTTP endpoint. The
// daemon runs one of these alongside its other goroutines so external
// processes (the standalone `cerberus mcp` subprocess, CLI commands)
// can drive runtime operations without loading their own config.
//
// The server is intentionally dependency-free: stdlib net/http +
// net.Listen("unix", ...). No framing, no gRPC, no hand-rolled
// JSON-RPC (portfolio baseline forbids the latter).
type SocketServer struct {
	client Client
	path   string
	logger *slog.Logger

	// peerCreds reads a connection's peer credentials, and uid is the uid
	// a peer must have. Tests replace the reader.
	peerCreds func(net.Conn) peerCred
	uid       int

	// Lifecycle guards.
	mu       sync.Mutex
	listener net.Listener
	srv      *http.Server

	// readHeaderTimeout limits the time given to read request headers
	// from the socket. Local-only traffic, kept small to fail fast on
	// wedged clients. Exposed for test injection via option.
	readHeaderTimeout time.Duration
}

// SocketServerOption tunes construction of a SocketServer.
type SocketServerOption func(*SocketServer)

// WithLogger attaches a slog logger. Defaults to slog.Default().
func WithLogger(l *slog.Logger) SocketServerOption {
	return func(s *SocketServer) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithReadHeaderTimeout overrides the default 5s read-header timeout.
func WithReadHeaderTimeout(d time.Duration) SocketServerOption {
	return func(s *SocketServer) {
		if d > 0 {
			s.readHeaderTimeout = d
		}
	}
}

// NewSocketServer constructs a SocketServer. The socket is not opened
// until Run is called. path is the unix-socket path
// (`~/.cerberus/cerberus.sock` in production; temp-dir paths in tests).
func NewSocketServer(client Client, path string, opts ...SocketServerOption) *SocketServer {
	s := &SocketServer{
		client:            client,
		path:              path,
		logger:            slog.Default(),
		readHeaderTimeout: 5 * time.Second,
		peerCreds:         kernelPeerCred,
		uid:               os.Getuid(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Run opens the socket and serves until ctx is canceled. Unlinks any
// stale socket file first (the daemon lock-guard has already enforced
// the single-daemon invariant by the time this runs, so it's safe to
// take over the path).
//
// Returns only when the server has fully shut down or failed to start.
func (s *SocketServer) Run(ctx context.Context) error {
	if err := s.listen(); err != nil {
		return err
	}
	defer func() {
		if err := s.cleanupSocket(); err != nil {
			s.logger.Warn("daemon.socket.cleanup_failed", "error", err.Error())
		}
	}()

	mux := s.routes()
	srv := &http.Server{
		Handler:           s.wrap(mux),
		ReadHeaderTimeout: s.readHeaderTimeout,
		// Each connection's peer credentials are read once, from the
		// kernel, and every request on it is checked against them.
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return withPeer(ctx, s.peerCreds(c))
		},
	}

	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(s.listener)
	}()

	s.logger.Info("daemon.socket.listening", "path", s.path)

	select {
	case <-ctx.Done():
		s.logger.Info("daemon.socket.shutdown", "reason", "context canceled")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			s.logger.Warn("daemon.socket.shutdown_error", "error", err.Error())
		}
		// Drain the serve goroutine.
		<-serveErr
		return ctx.Err()
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("socket server: %w", err)
		}
		return nil
	}
}

// Addr returns the socket path once Run has opened the listener.
// Returns "" before Run or after shutdown.
func (s *SocketServer) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *SocketServer) listen() error {
	// Best-effort cleanup of a stale socket file. If a previous daemon
	// crashed without calling Release(), the file may linger. The
	// daemon-lock flock is the authoritative single-daemon guard, so
	// by the time Run is invoked we are the uncontested owner and can
	// safely Unlink.
	if err := s.cleanupSocket(); err != nil {
		return err
	}
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		return fmt.Errorf("listen unix %q: %w", s.path, err)
	}
	// Enforce user-only access. Daemon + MCP subprocess share the
	// same uid so the subprocess can still read/write.
	if chmodErr := os.Chmod(s.path, 0600); chmodErr != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod socket %q: %w", s.path, chmodErr)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	return nil
}

func (s *SocketServer) cleanupSocket() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove socket %q: %w", s.path, err)
	}
	return nil
}

// wrap adds API-version enforcement + access logging to the mux.
func (s *SocketServer) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(APIHeaderName); got != "" && got != APIVersion {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("unsupported %s: %s (server expects %s)", APIHeaderName, got, APIVersion))
			return
		}
		w.Header().Set(APIHeaderName, APIVersion)
		// The request begins first, so a refusal renders in its scope and
		// is marked rendered like any other.
		w, r = BeginHTTPRequest(w, r, SurfaceSocket)
		// The socket is 0600, and the kernel's word on who connected is
		// checked as well: a uid other than the daemon's is refused, and so
		// is a connection whose peer cannot be read.
		if err := s.checkPeer(r.Context()); err != nil {
			err = scopeError(redact.ScopeFrom(r.Context()), err)
			s.logger.Warn("daemon.socket.peer_refused", "method", r.Method, "path", r.URL.Path, "error", err.Error())
			writeServiceError(w, http.StatusForbidden, err)
			return
		}
		s.logger.Info("daemon.socket.request", "method", r.Method, "path", r.URL.Path)
		h.ServeHTTP(w, r)
	})
}

func (s *SocketServer) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// /health — daemon + v2 resource health.
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ping", s.handlePing)
	mux.HandleFunc("/whoami", s.handleWhoAmI)
	mux.HandleFunc("/approvals", s.handleApprovals)
	mux.HandleFunc("/approvals/", s.handleApprovals)

	// /project, /resource, /pipeline list + run — active v2 surface.
	mux.HandleFunc("/projects", s.handleProjects)
	mux.HandleFunc("/registry/diagnostics", s.handleRegistryDiagnostics)
	mux.HandleFunc("/resources", s.handleResources)
	mux.HandleFunc("/resources/", s.handleResourcesID)
	mux.HandleFunc("/pipelines", s.handlePipelines)
	mux.HandleFunc("/pipelines/", s.handlePipelinesID)
	mux.HandleFunc("/connectors", s.handleConnectors)
	mux.HandleFunc("/connectors/live", s.handleConnectorsLive)
	mux.HandleFunc("/connectors/", s.handleConnectorsID)
	mux.HandleFunc("/plugins/connectors", s.handleManagedPluginConnectors)
	mux.HandleFunc("/plugins/connectors/", s.handleManagedPluginConnectorsID)
	mux.HandleFunc("/plugins/connectors/health", s.handlePluginDirRetired)
	mux.HandleFunc("/plugins/connectors/operations/", s.handlePluginDirRetired)

	return mux
}

// ---- handlers ----

func (s *SocketServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := r.URL.Query().Get("resource_id")
	h, err := s.client.Health(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *SocketServer) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, DaemonStatus{DaemonRunning: true, SocketReady: true})
}

func (s *SocketServer) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := s.client.ListProjects(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []ProjectInfo{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *SocketServer) handleRegistryDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	diag, err := s.client.ResolveDiagnostics(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if diag == nil {
		diag = &ResolveDiagnostics{}
	}
	writeJSON(w, http.StatusOK, diag)
}

func (s *SocketServer) handleResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	args := ResourceListArgs{
		ProjectID: r.URL.Query().Get("project_id"),
		Connector: r.URL.Query().Get("connector"),
		Tag:       r.URL.Query().Get("tag"),
	}
	list, err := s.client.ListResources(r.Context(), args)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []ResourceInfo{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *SocketServer) handleResourcesID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/resources/")
	if rest == "" {
		writeJSONError(w, http.StatusBadRequest, "resource id required")
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	switch action {
	case "doctor":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		st, err := s.client.GetResourceDoctor(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, st)
	case "inspect":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		st, err := s.client.GetResourceInspect(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, st)
	case "status":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		st, err := s.client.GetResourceRuntime(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, st)
	case "logs":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		lines := 50
		if raw := r.URL.Query().Get("lines"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				lines = n
			}
		}
		stream := r.URL.Query().Get("stream")
		out, err := s.client.ResourceLogs(r.Context(), id, lines, stream)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "deploy", "apply", "reload", "stop", "sync", "remove":
		s.handleResourceMutation(w, r, id, action)
	default:
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown resource action %q", action))
	}
}

func (s *SocketServer) handlePipelines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := s.client.ListPipelines(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []PipelineInfo{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *SocketServer) handlePipelinesID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/pipelines/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	if action == "" {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		out, err := s.client.GetPipeline(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if action != "run" {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown pipeline action %q", action))
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	opts, decodeErr := decodeMutationOptions(r.Body)
	if decodeErr != nil {
		writeJSONError(w, http.StatusBadRequest, decodeErr.Error())
		return
	}
	if s.handleStream(w, r, func(ctx context.Context) (interface{}, error) {
		return s.client.RunPipeline(ctx, id, opts...)
	}) {
		return
	}
	res, err := s.client.RunPipeline(r.Context(), id, opts...)
	if err != nil {
		writeServiceError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// resourceMutations are the resource verbs the socket serves, and whether
// each streams progress.
var resourceMutations = map[string]struct {
	stream bool
	call   func(Client, context.Context, string, ...MutationOption) (*OpResult, error)
}{
	"deploy": {true, Client.DeployResource},
	"apply":  {true, Client.ApplyResource},
	"reload": {false, Client.ReloadResource},
	"stop":   {false, Client.StopResource},
	"sync":   {true, Client.SyncResource},
	"remove": {false, Client.RemoveResource},
}

// handleResourceMutation serves one resource mutation. Its options —
// the caller's acknowledgment among them — come from the request body, and a
// refusal keeps its code and status.
func (s *SocketServer) handleResourceMutation(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mutation := resourceMutations[action]
	opts, decodeErr := decodeMutationOptions(r.Body)
	if decodeErr != nil {
		writeJSONError(w, http.StatusBadRequest, decodeErr.Error())
		return
	}
	if mutation.stream && s.handleStream(w, r, func(ctx context.Context) (interface{}, error) {
		return mutation.call(s.client, ctx, id, opts...)
	}) {
		return
	}
	res, err := mutation.call(s.client, r.Context(), id, opts...)
	if err != nil {
		writeServiceError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *SocketServer) handleConnectors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := s.client.ListConnectors(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []contract.Definition{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *SocketServer) handleConnectorsLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ids, err := s.client.ListLiveConnectors(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, ids)
}

func (s *SocketServer) handleConnectorsID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/connectors/")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "operations" || parts[2] == "" {
		writeJSONError(w, http.StatusNotFound, "expected /connectors/{id}/operations/{operation}")
		return
	}

	var args ExternalConnectorOperationArgs
	if err := decodeJSONBody(r, &args); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	args.Connector = parts[0]
	args.Operation = parts[2]
	if args.Config == nil {
		args.Config = map[string]any{}
	}

	if s.handleStream(w, r, func(ctx context.Context) (interface{}, error) {
		return s.client.ExecuteConnectorOperation(ctx, args)
	}) {
		return
	}
	out, err := s.client.ExecuteConnectorOperation(r.Context(), args)
	if err != nil {
		writeServiceError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePluginDirRetired answers the two retired one-shot routes,
// /plugins/connectors/health and /plugins/connectors/operations/, which used
// to install, load and run whatever directory plugin_dir named. They stay
// registered so a caller gets the reason rather than a routing 404.
func (s *SocketServer) handlePluginDirRetired(w http.ResponseWriter, _ *http.Request) {
	writeJSONError(w, http.StatusGone, PluginDirRetired)
}

func (s *SocketServer) handleManagedPluginConnectors(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := s.client.ListManagedPlugins(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *SocketServer) handleManagedPluginConnectorsID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/plugins/connectors/")
	if rest == "" || rest == r.URL.Path {
		writeJSONError(w, http.StatusNotFound, "expected /plugins/connectors/{id}/...")
		return
	}
	parts := strings.Split(rest, "/")
	// Install by path is retired: installing is a review confirmed on the
	// operator's terminal, which no socket caller can give. The route stays
	// so an older CLI gets the reason rather than a routing 404.
	if len(parts) == 1 && parts[0] == "install" {
		writeJSONError(w, http.StatusGone, PluginInstallRetired)
		return
	}
	if len(parts) < 2 {
		writeJSONError(w, http.StatusNotFound, "expected /plugins/connectors/{id}/...")
		return
	}
	id := parts[0]
	action := parts[1]

	switch action {
	case "reload":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if s.handleStream(w, r, func(ctx context.Context) (interface{}, error) {
			return s.client.ReloadManagedPlugin(ctx, id)
		}) {
			return
		}
		out, err := s.client.ReloadManagedPlugin(r.Context(), id)
		if err != nil {
			writeServiceError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "load":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if s.handleStream(w, r, func(ctx context.Context) (interface{}, error) {
			return s.client.LoadManagedPlugin(ctx, id)
		}) {
			return
		}
		out, err := s.client.LoadManagedPlugin(r.Context(), id)
		if err != nil {
			writeServiceError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "unload":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if s.handleStream(w, r, func(ctx context.Context) (interface{}, error) {
			return s.client.UnloadManagedPlugin(ctx, id)
		}) {
			return
		}
		out, err := s.client.UnloadManagedPlugin(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "uninstall":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if s.handleStream(w, r, func(ctx context.Context) (interface{}, error) {
			return s.client.UninstallManagedPlugin(ctx, id)
		}) {
			return
		}
		out, err := s.client.UninstallManagedPlugin(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "health":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		out, err := s.client.ManagedPluginHealth(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "operations":
		if r.Method != http.MethodPost || len(parts) < 3 {
			writeJSONError(w, http.StatusNotFound, "expected /plugins/connectors/{id}/operations/{operation}")
			return
		}
		var args PluginConnectorExecArgs
		if err := decodeJSONBody(r, &args); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if args.PluginDir != "" {
			writeJSONError(w, http.StatusBadRequest, PluginDirNotAccepted)
			return
		}
		args.Operation = parts[2]
		if s.handleStream(w, r, func(ctx context.Context) (interface{}, error) {
			return s.client.ExecuteManagedPlugin(ctx, id, args)
		}) {
			return
		}
		out, err := s.client.ExecuteManagedPlugin(r.Context(), id, args)
		if err != nil {
			writeServiceError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown managed plugin action")
	}
}

// ---- helpers ----

// decodeJSONBody decodes an HTTP request JSON body. Empty bodies are
// treated as a zero value (e.g. for start, which takes no args).
func decodeJSONBody(r *http.Request, dst interface{}) error {
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

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	switch e := body.(type) {
	case ErrorResponse:
		e.markRendered(ResponseScope(w), e.Error)
		body = e
	case *ErrorResponse:
		e.markRendered(ResponseScope(w), e.Error)
	}
	data, err := ResponseScope(w).MarshalIndent(body, "", "  ")
	if err == nil {
		_ = json.NewEncoder(w).Encode(json.RawMessage(data))
	}
}

func (s *SocketServer) handleStream(w http.ResponseWriter, r *http.Request, fn func(context.Context) (interface{}, error)) bool {
	if !wantsProgressStream(r) {
		return false
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming not supported")
		return true
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	bw := bufio.NewWriter(w)
	scope := redact.ScopeFrom(r.Context())
	writeEnvelope := func(env StreamEnvelope) {
		data, err := scope.Marshal(env)
		if err != nil {
			return
		}
		data = append(data, '\n')
		if _, err := bw.Write(data); err != nil {
			return
		}
		_ = bw.Flush()
		flusher.Flush()
	}

	ctx := gmcp.WithNotifier(r.Context(), func(n gmcp.Notification) {
		writeEnvelope(StreamEnvelope{Type: "notification", Notification: &n})
	})
	result, err := fn(ctx)
	if err != nil {
		env := StreamEnvelope{Type: "error", Error: err.Error(), connectorErrorWire: connectorErrorWireFor(err)}
		env.markRendered(scope, env.Error)
		writeEnvelope(env)
		return true
	}
	data, err := scope.Marshal(result)
	if err != nil {
		writeEnvelope(StreamEnvelope{Type: "error", Error: fmt.Sprintf("marshal result: %v", err)})
		return true
	}
	writeEnvelope(StreamEnvelope{Type: "result", Result: data})
	return true
}

func wantsProgressStream(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get(ProgressHeaderName)), "1")
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Success: false, Error: msg})
}

// decodeMutationOptions parses a mutation's request body into options. An
// empty body is valid and yields none — which carries no acknowledgment, so
// a gated operation is refused.
func decodeMutationOptions(body io.Reader) ([]MutationOption, error) {
	if body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read mutation body: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var opts MutationOpts
	if err := json.Unmarshal(data, &opts); err != nil {
		return nil, fmt.Errorf("decode mutation body: %w", err)
	}
	options := []MutationOption{WithAcknowledged(opts.Acknowledged), WithApprovalID(opts.ApprovalID)}
	if opts.InstallAfterBuildOverride != nil {
		options = append(options, WithInstallAfterBuildOverride(*opts.InstallAfterBuildOverride))
	}
	return options, nil
}

// checkPeer refuses a request whose connection's peer is not the daemon's
// own user, or cannot be read.
func (s *SocketServer) checkPeer(ctx context.Context) error {
	args := ExternalConnectorOperationArgs{Connector: "socket", Operation: "connect"}
	peer, ok := peerFrom(ctx)
	switch {
	// Cerberus's own guidance, rendered once where it is made (S2-6); the
	// uids are numbers the kernel reported, and the kernel's error is the
	// only text that goes through the rules.
	case !ok:
		return externalConnectorError(args, ExternalConnectorPrincipalRefused, redact.Guidance("the connection's peer credentials were not read; refusing"))
	case peer.err != nil:
		return externalConnectorError(args, ExternalConnectorPrincipalRefused, redact.GuidanceWrap(peer.err, "the connection's peer credentials could not be read, so this daemon refuses it"))
	case peer.uid != s.uid:
		return externalConnectorError(args, ExternalConnectorPrincipalRefused, redact.Guidance("the connecting process runs as uid %d, and this daemon serves only its own user (uid %d); run cerberus as that user", peer.uid, s.uid))
	}
	return nil
}

// handleWhoAmI answers with the principal the daemon gives this request: the
// caller's claim, marked self-reported, with the uid the kernel reported.
func (s *SocketServer) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	p, _ := PrincipalFrom(r.Context())
	writeJSON(w, http.StatusOK, p)
}

// handleApprovals answers GET /approvals and GET /approvals/{id} from the
// daemon's broker. Read-only: deciding an approval is P3-4, and never over
// a surface an agent can drive.
func (s *SocketServer) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	broker := ProcessBroker()
	if broker == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "this daemon has no approval broker; see its log for why the approvals store did not open")
		return
	}
	id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/approvals"), "/")
	if id == "" {
		writeJSON(w, http.StatusOK, ApprovalList{Approvals: broker.List(), Problems: broker.Problems()})
		return
	}
	a, ok := broker.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("no approval %q", id))
		return
	}
	writeJSON(w, http.StatusOK, a)
}
