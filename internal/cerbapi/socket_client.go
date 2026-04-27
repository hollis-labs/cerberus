package cerbapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// SocketClient satisfies Client by forwarding each call over a unix
// socket to a daemon-hosted SocketServer.
//
// The client is safe for concurrent use (http.Client is). It holds no
// config or service state of its own — that's the entire point of the
// CERB-2 redesign.
type SocketClient struct {
	http    *http.Client
	baseURL string // dummy scheme+host, DialContext routes to the socket
	logger  *slog.Logger

	// dialPath is retained purely for error-message context.
	dialPath string
}

// SocketClientOption tunes construction of a SocketClient.
type SocketClientOption func(*SocketClient)

// WithClientLogger sets the logger used for client-side events.
func WithClientLogger(l *slog.Logger) SocketClientOption {
	return func(c *SocketClient) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithClientTimeout overrides the default per-request timeout.
func WithClientTimeout(d time.Duration) SocketClientOption {
	return func(c *SocketClient) {
		if d > 0 {
			c.http.Timeout = d
		}
	}
}

// WithClientDialTimeout overrides the default dial timeout.
func WithClientDialTimeout(d time.Duration) SocketClientOption {
	return func(c *SocketClient) {
		if d <= 0 {
			return
		}
		tr, ok := c.http.Transport.(*http.Transport)
		if !ok {
			return
		}
		dialer := &net.Dialer{Timeout: d}
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", c.dialPath)
		}
	}
}

// NewSocketClient constructs a SocketClient bound to the given unix
// socket path. It does NOT eagerly dial — the first RPC attempt is the
// connectivity check.
func NewSocketClient(socketPath string, opts ...SocketClientOption) *SocketClient {
	dialer := &net.Dialer{Timeout: DialTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	c := &SocketClient{
		http: &http.Client{
			Transport: transport,
			Timeout:   RequestTimeout,
		},
		// The unix-socket transport ignores host/scheme, but net/http
		// insists on a valid URL — we pick a stable placeholder.
		baseURL:  "http://cerberus-daemon",
		logger:   slog.Default(),
		dialPath: socketPath,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Ping performs a lightweight GET /health to verify the daemon is
// reachable. Callers (e.g. the `cerberus mcp` entrypoint) can use this
// to fail fast with a clear error rather than deferring the problem to
// the first tool call.
func (c *SocketClient) Ping(ctx context.Context) error {
	_, err := c.Health(ctx, "")
	return err
}

// DialPath returns the unix-socket path this client is bound to. Used
// by multi-client tests that want to construct additional clients
// against the same daemon socket.
func (c *SocketClient) DialPath() string {
	return c.dialPath
}

// ---- Client impl ----

func (c *SocketClient) ListServices(ctx context.Context) ([]ServiceStatus, error) {
	var out []ServiceStatus
	if err := c.doJSON(ctx, http.MethodGet, "/services", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *SocketClient) GetService(ctx context.Context, id string) (*ServiceStatus, error) {
	if id == "" {
		return nil, errors.New("service id required")
	}
	var out ServiceStatus
	if err := c.doJSON(ctx, http.MethodGet, "/services/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) StartService(ctx context.Context, id string) (*OpResult, error) {
	if id == "" {
		return nil, errors.New("service id required")
	}
	var out OpResult
	if err := c.doJSON(ctx, http.MethodPost, "/services/"+url.PathEscape(id)+"/start", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) StopService(ctx context.Context, id string, audit AuditContext) (*OpResult, error) {
	if id == "" {
		return nil, errors.New("service id required")
	}
	body := stopBody(audit)
	var out OpResult
	if err := c.doJSON(ctx, http.MethodPost, "/services/"+url.PathEscape(id)+"/stop", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) RestartService(ctx context.Context, id string, args RestartServiceArgs) (*OpResult, error) {
	if id == "" {
		return nil, errors.New("service id required")
	}
	body := restartBody{
		stopBody: stopBody(args.Audit),
		Force:    args.Force,
	}
	var out OpResult
	if err := c.doJSON(ctx, http.MethodPost, "/services/"+url.PathEscape(id)+"/restart", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) RebuildService(ctx context.Context, id string, args RebuildServiceArgs) (*OpResult, error) {
	if id == "" {
		return nil, errors.New("service id required")
	}
	body := rebuildBody{
		stopBody: stopBody(args.Audit),
		Force:    args.Force,
	}
	var out OpResult
	if err := c.doJSON(ctx, http.MethodPost, "/services/"+url.PathEscape(id)+"/rebuild", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) BuildService(ctx context.Context, id string) (*OpResult, error) {
	if id == "" {
		return nil, errors.New("service id required")
	}
	var out OpResult
	if err := c.doJSON(ctx, http.MethodPost, "/services/"+url.PathEscape(id)+"/build", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) ServiceLogs(ctx context.Context, id string, lines int) (*LogLines, error) {
	if id == "" {
		return nil, errors.New("service id required")
	}
	q := url.Values{}
	if lines > 0 {
		q.Set("lines", strconv.Itoa(lines))
	}
	path := "/services/" + url.PathEscape(id) + "/logs"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out LogLines
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) ResourceLogs(ctx context.Context, id string, lines int, stream string) (*LogLines, error) {
	if id == "" {
		return nil, errors.New("resource id required")
	}
	q := url.Values{}
	if lines > 0 {
		q.Set("lines", strconv.Itoa(lines))
	}
	if stream != "" {
		q.Set("stream", stream)
	}
	path := "/resources/" + url.PathEscape(id) + "/logs"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out LogLines
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) Health(ctx context.Context, id string) (*DaemonHealth, error) {
	path := "/health"
	if id != "" {
		path += "?service_id=" + url.QueryEscape(id)
	}
	var out DaemonHealth
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	var out []ProjectInfo
	if err := c.doJSON(ctx, http.MethodGet, "/projects", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *SocketClient) ListResources(ctx context.Context, args ResourceListArgs) ([]ResourceInfo, error) {
	q := url.Values{}
	if args.ProjectID != "" {
		q.Set("project_id", args.ProjectID)
	}
	if args.Connector != "" {
		q.Set("connector", args.Connector)
	}
	if args.Tag != "" {
		q.Set("tag", args.Tag)
	}
	path := "/resources"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out []ResourceInfo
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *SocketClient) GetResourceRuntime(ctx context.Context, id string) (*ResourceRuntimeStatus, error) {
	if id == "" {
		return nil, errors.New("resource id required")
	}
	var out ResourceRuntimeStatus
	if err := c.doJSON(ctx, http.MethodGet, "/resources/"+url.PathEscape(id)+"/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) GetResourceInspect(ctx context.Context, id string) (*ResourceInspect, error) {
	if id == "" {
		return nil, errors.New("resource id required")
	}
	var out ResourceInspect
	if err := c.doJSON(ctx, http.MethodGet, "/resources/"+url.PathEscape(id)+"/inspect", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) GetResourceDoctor(ctx context.Context, id string) (*ResourceDoctor, error) {
	if id == "" {
		return nil, errors.New("resource id required")
	}
	var out ResourceDoctor
	if err := c.doJSON(ctx, http.MethodGet, "/resources/"+url.PathEscape(id)+"/doctor", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) ApplyResource(ctx context.Context, id string) (*OpResult, error) {
	if id == "" {
		return nil, errors.New("resource id required")
	}
	var out OpResult
	if err := c.doJSON(ctx, http.MethodPost, "/resources/"+url.PathEscape(id)+"/apply", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) ReloadResource(ctx context.Context, id string) (*OpResult, error) {
	if id == "" {
		return nil, errors.New("resource id required")
	}
	var out OpResult
	if err := c.doJSON(ctx, http.MethodPost, "/resources/"+url.PathEscape(id)+"/reload", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) SyncResource(ctx context.Context, id string) (*OpResult, error) {
	if id == "" {
		return nil, errors.New("resource id required")
	}
	var out OpResult
	if err := c.doJSON(ctx, http.MethodPost, "/resources/"+url.PathEscape(id)+"/sync", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) RemoveResource(ctx context.Context, id string) (*OpResult, error) {
	if id == "" {
		return nil, errors.New("resource id required")
	}
	var out OpResult
	if err := c.doJSON(ctx, http.MethodPost, "/resources/"+url.PathEscape(id)+"/remove", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) ListPipelines(ctx context.Context) ([]PipelineInfo, error) {
	var out []PipelineInfo
	if err := c.doJSON(ctx, http.MethodGet, "/pipelines", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *SocketClient) RunPipeline(ctx context.Context, id string) (*PipelineRunResult, error) {
	if id == "" {
		return nil, errors.New("pipeline id required")
	}
	var out PipelineRunResult
	if err := c.doJSON(ctx, http.MethodPost, "/pipelines/"+url.PathEscape(id)+"/run", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- transport ----

// doJSON marshals body (if non-nil) as JSON, issues the request against
// the daemon socket, and decodes the JSON response into out (if
// non-nil). On a non-2xx response, it decodes ErrorResponse and wraps
// it into a daemon-context error.
func (c *SocketClient) doJSON(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	// baseURL is a package-internal placeholder; the transport's
	// DialContext routes every request to the unix socket bound at
	// construction time, so there is no user-controlled URL here.
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader) //nolint:gosec // unix-socket transport, not a real HTTP destination
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set(APIHeaderName, APIVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req) //nolint:gosec // see NewRequestWithContext note
	if err != nil {
		// Distinguish "daemon not running" (dial failure) from other
		// errors so the caller can surface the operator-facing hint.
		c.logger.Warn("client.socket.dial_failed",
			"path", c.dialPath,
			"error", err.Error())
		return &DaemonUnreachableError{Path: c.dialPath, Err: err}
	}
	defer resp.Body.Close() //nolint:errcheck

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB cap
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp ErrorResponse
		if jerr := json.Unmarshal(data, &errResp); jerr == nil && errResp.Error != "" {
			return fmt.Errorf("daemon: %s", errResp.Error)
		}
		return fmt.Errorf("daemon: HTTP %d: %s", resp.StatusCode, string(data))
	}

	if out == nil {
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	if jerr := json.Unmarshal(data, out); jerr != nil {
		return fmt.Errorf("decode response: %w", jerr)
	}
	return nil
}

// DaemonUnreachableError is returned by SocketClient when dial to the
// daemon socket fails. Callers (particularly `cerberus mcp`) can use
// errors.As to format a structured "daemon not running" error rather
// than leaking low-level syscall details.
type DaemonUnreachableError struct {
	Path string
	Err  error
}

func (e *DaemonUnreachableError) Error() string {
	return fmt.Sprintf("cerberus daemon not running at %s; start with 'cerberus daemon' (%v)", e.Path, e.Err)
}

func (e *DaemonUnreachableError) Unwrap() error { return e.Err }
