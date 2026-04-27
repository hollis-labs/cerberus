package cerbapi

import (
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
)

// SocketServer exposes a Client over a unix-socket HTTP endpoint. The
// daemon runs one of these alongside its other goroutines so external
// processes (the standalone `cerberus mcp` subprocess, CLI commands)
// can drive lifecycle ops without loading their own config.
//
// The server is intentionally dependency-free: stdlib net/http +
// net.Listen("unix", ...). No framing, no gRPC, no hand-rolled
// JSON-RPC (portfolio baseline forbids the latter).
type SocketServer struct {
	client Client
	path   string
	logger *slog.Logger

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

	mux := s.routes()
	srv := &http.Server{
		Handler:           s.wrap(mux),
		ReadHeaderTimeout: s.readHeaderTimeout,
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
		_ = s.cleanupSocket()
		return ctx.Err()
	case err := <-serveErr:
		_ = s.cleanupSocket()
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
	if _, err := os.Stat(s.path); err == nil {
		if rmErr := os.Remove(s.path); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("remove stale socket %q: %w", s.path, rmErr)
		}
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
	if _, err := os.Stat(s.path); err == nil {
		return os.Remove(s.path)
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
		s.logger.Info("daemon.socket.request", "method", r.Method, "path", r.URL.Path)
		h.ServeHTTP(w, r)
	})
}

func (s *SocketServer) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// /services — GET list, with {id} subroutes.
	mux.HandleFunc("/services", s.handleServicesRoot)
	mux.HandleFunc("/services/", s.handleServicesID)

	// /health
	mux.HandleFunc("/health", s.handleHealth)

	// /project, /resource, /pipeline list + run.
	mux.HandleFunc("/projects", s.handleProjects)
	mux.HandleFunc("/resources", s.handleResources)
	mux.HandleFunc("/resources/", s.handleResourcesID)
	mux.HandleFunc("/pipelines", s.handlePipelines)
	mux.HandleFunc("/pipelines/", s.handlePipelinesID)

	return mux
}

// ---- handlers ----

func (s *SocketServer) handleServicesRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := s.client.ListServices(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *SocketServer) handleServicesID(w http.ResponseWriter, r *http.Request) {
	// Parse /services/{id}[/action]
	rest := strings.TrimPrefix(r.URL.Path, "/services/")
	if rest == "" {
		writeJSONError(w, http.StatusBadRequest, "service id required")
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
		// GET /services/{id}
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		st, err := s.client.GetService(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, st)
	case "start":
		s.postOp(w, r, id, func(ctx context.Context) (*OpResult, error) {
			return s.client.StartService(ctx, id)
		})
	case "stop":
		s.postStop(w, r, id)
	case "restart":
		s.postRestart(w, r, id)
	case "rebuild":
		s.postRebuild(w, r, id)
	case "build":
		s.postOp(w, r, id, func(ctx context.Context) (*OpResult, error) {
			return s.client.BuildService(ctx, id)
		})
	case "logs":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		lines := 50
		if v := r.URL.Query().Get("lines"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				lines = n
			}
		}
		ll, err := s.client.ServiceLogs(r.Context(), id, lines)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ll)
	default:
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown service action %q", action))
	}
}

func (s *SocketServer) postOp(w http.ResponseWriter, r *http.Request, _ string, fn func(ctx context.Context) (*OpResult, error)) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	res, err := fn(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// stopBody is the request body shape for POST /services/{id}/stop.
// Structurally identical to AuditContext so callers can use a direct
// type conversion (AuditContext(body)) instead of re-packing fields.
type stopBody struct {
	Reason    string `json:"reason"`
	TaskID    string `json:"task_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type restartBody struct {
	stopBody
	Force bool `json:"force,omitempty"`
}

type rebuildBody struct {
	stopBody
	Force bool `json:"force,omitempty"`
}

func (s *SocketServer) postStop(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body stopBody
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.client.StopService(r.Context(), id, AuditContext(body))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *SocketServer) postRestart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body restartBody
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.client.RestartService(r.Context(), id, RestartServiceArgs{
		Audit: AuditContext(body.stopBody),
		Force: body.Force,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *SocketServer) postRebuild(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body rebuildBody
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.client.RebuildService(r.Context(), id, RebuildServiceArgs{
		Audit: AuditContext(body.stopBody),
		Force: body.Force,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *SocketServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := r.URL.Query().Get("service_id")
	h, err := s.client.Health(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h)
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
	case "apply":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		res, err := s.client.ApplyResource(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
	case "sync":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		res, err := s.client.SyncResource(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
	case "remove":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		res, err := s.client.RemoveResource(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
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
	if action != "run" {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown pipeline action %q", action))
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	res, err := s.client.RunPipeline(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
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
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Success: false, Error: msg})
}
