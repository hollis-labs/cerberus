package cerbapi

import (
	"bufio"
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
	"syscall"
	"time"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	gmcp "github.com/hollis-labs/go-mcp/server"
)

// SocketClient satisfies Client by forwarding each call over a unix
// socket to a daemon-hosted SocketServer.
//
// The client is safe for concurrent use (http.Client is). It holds no
// config or service state of its own — that's the entire point of the
// CERB-2 redesign. The active runtime surface is the v2 resource lane.
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

// WithClientTimeout overrides the default per-request timeout. Zero disables
// the client timeout; cancellation and deadlines still follow the request context.
func WithClientTimeout(d time.Duration) SocketClientOption {
	return func(c *SocketClient) {
		if d >= 0 {
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
	var out DaemonStatus
	err := c.doJSON(ctx, http.MethodGet, "/ping", nil, &out)
	return err
}

// DialPath returns the unix-socket path this client is bound to. Used
// by multi-client tests that want to construct additional clients
// against the same daemon socket.
func (c *SocketClient) DialPath() string {
	return c.dialPath
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
		path += "?resource_id=" + url.QueryEscape(id)
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

func (c *SocketClient) ResolveDiagnostics(ctx context.Context) (*ResolveDiagnostics, error) {
	var out ResolveDiagnostics
	if err := c.doJSON(ctx, http.MethodGet, "/registry/diagnostics", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
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

// mutation posts a resource mutation with its options in the body. The
// acknowledgment travels explicitly, per call, so a daemon-routed call is
// gated on exactly what the caller sent.
func (c *SocketClient) mutation(ctx context.Context, id, verb string, stream bool, options []MutationOption) (*OpResult, error) {
	if id == "" {
		return nil, errors.New("resource id required")
	}
	body := ApplyMutationOptions(options)
	path := "/resources/" + url.PathEscape(id) + "/" + verb
	var out OpResult
	do := c.doJSON
	if stream {
		do = c.doJSONStream
	}
	if err := do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) DeployResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return c.mutation(ctx, id, "deploy", true, options)
}

func (c *SocketClient) ApplyResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return c.mutation(ctx, id, "apply", true, options)
}

func (c *SocketClient) ReloadResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return c.mutation(ctx, id, "reload", false, options)
}

func (c *SocketClient) StopResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return c.mutation(ctx, id, "stop", false, options)
}

func (c *SocketClient) SyncResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return c.mutation(ctx, id, "sync", true, options)
}

func (c *SocketClient) RemoveResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return c.mutation(ctx, id, "remove", false, options)
}

func (c *SocketClient) ListPipelines(ctx context.Context) ([]PipelineInfo, error) {
	var out []PipelineInfo
	if err := c.doJSON(ctx, http.MethodGet, "/pipelines", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *SocketClient) GetPipeline(ctx context.Context, id string) (*PipelineDetail, error) {
	if id == "" {
		return nil, errors.New("pipeline id required")
	}
	var out *PipelineDetail
	if err := c.doJSON(ctx, http.MethodGet, "/pipelines/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *SocketClient) RunPipeline(ctx context.Context, id string, options ...MutationOption) (*PipelineRunResult, error) {
	if id == "" {
		return nil, errors.New("pipeline id required")
	}
	var out PipelineRunResult
	if err := c.doJSONStream(ctx, http.MethodPost, "/pipelines/"+url.PathEscape(id)+"/run", ApplyMutationOptions(options), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) ListConnectors(ctx context.Context) ([]contract.Definition, error) {
	var out []contract.Definition
	if err := c.doJSON(ctx, http.MethodGet, "/connectors", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *SocketClient) ListLiveConnectors(ctx context.Context) ([]string, error) {
	var out []string
	if err := c.doJSON(ctx, http.MethodGet, "/connectors/live", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *SocketClient) ExecuteConnectorOperation(ctx context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	var out ExternalConnectorOperationResult
	err := c.executeConnectorOperation(ctx, args, &out)
	return out, err
}

// ExecuteTypedConnectorOperation retains built-in DTOs for callers that format
// provider-specific fields. Generic MCP/plugin callers keep their open payloads.
func (c *SocketClient) ExecuteTypedConnectorOperation(ctx context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	var out struct {
		Connector string          `json:"connector"`
		Operation string          `json:"operation"`
		Data      json.RawMessage `json:"data"`
	}
	if err := c.executeConnectorOperation(ctx, args, &out); err != nil {
		return ExternalConnectorOperationResult{}, err
	}
	data, err := decodeConnectorPayload(args, out.Data)
	if err != nil {
		return ExternalConnectorOperationResult{}, fmt.Errorf("decode %s %s payload: %w", args.Connector, args.Operation, err)
	}
	return ExternalConnectorOperationResult{Connector: out.Connector, Operation: out.Operation, Data: data}, nil
}

func (c *SocketClient) executeConnectorOperation(ctx context.Context, args ExternalConnectorOperationArgs, out any) error {
	if args.Connector == "" {
		return errors.New("connector id required")
	}
	if args.Operation == "" {
		return errors.New("connector operation required")
	}
	path := "/connectors/" + url.PathEscape(args.Connector) + "/operations/" + url.PathEscape(args.Operation)
	return c.doJSONStream(ctx, http.MethodPost, path, args, out)
}

func (c *SocketClient) InstallManagedPlugin(ctx context.Context, args PluginConnectorHealthArgs) (ManagedPluginConnectorState, error) {
	var out ManagedPluginConnectorState
	if err := c.doJSONStream(ctx, http.MethodPost, "/plugins/connectors/install", args, &out); err != nil {
		return ManagedPluginConnectorState{}, err
	}
	return out, nil
}

func (c *SocketClient) LoadManagedPlugin(ctx context.Context, id string) (ManagedPluginConnectorState, error) {
	var out ManagedPluginConnectorState
	if err := c.doJSONStream(ctx, http.MethodPost, "/plugins/connectors/"+url.PathEscape(id)+"/load", nil, &out); err != nil {
		return ManagedPluginConnectorState{}, err
	}
	return out, nil
}

func (c *SocketClient) UnloadManagedPlugin(ctx context.Context, id string) (ManagedPluginConnectorState, error) {
	var out ManagedPluginConnectorState
	if err := c.doJSONStream(ctx, http.MethodPost, "/plugins/connectors/"+url.PathEscape(id)+"/unload", nil, &out); err != nil {
		return ManagedPluginConnectorState{}, err
	}
	return out, nil
}

func (c *SocketClient) UninstallManagedPlugin(ctx context.Context, id string) (ManagedPluginConnectorState, error) {
	var out ManagedPluginConnectorState
	if err := c.doJSONStream(ctx, http.MethodPost, "/plugins/connectors/"+url.PathEscape(id)+"/uninstall", nil, &out); err != nil {
		return ManagedPluginConnectorState{}, err
	}
	return out, nil
}

func (c *SocketClient) ListManagedPlugins(ctx context.Context) ([]ManagedPluginConnectorState, error) {
	var out []ManagedPluginConnectorState
	if err := c.doJSON(ctx, http.MethodGet, "/plugins/connectors", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *SocketClient) ManagedPluginHealth(ctx context.Context, id string) (PluginConnectorHealth, error) {
	var out PluginConnectorHealth
	if err := c.doJSON(ctx, http.MethodGet, "/plugins/connectors/"+url.PathEscape(id)+"/health", nil, &out); err != nil {
		return PluginConnectorHealth{}, err
	}
	return out, nil
}

func (c *SocketClient) ExecuteManagedPlugin(ctx context.Context, id string, args PluginConnectorExecArgs) (ExternalConnectorOperationResult, error) {
	if args.Operation == "" {
		return ExternalConnectorOperationResult{}, errors.New("managed plugin operation required")
	}
	path := "/plugins/connectors/" + url.PathEscape(id) + "/operations/" + url.PathEscape(args.Operation)
	var out ExternalConnectorOperationResult
	if err := c.doJSONStream(ctx, http.MethodPost, path, args, &out); err != nil {
		return ExternalConnectorOperationResult{}, err
	}
	return out, nil
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
		return c.classifyTransportError(method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB cap
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp ErrorResponse
		if jerr := json.Unmarshal(data, &errResp); jerr == nil && errResp.Error != "" {
			return daemonError(errResp.Error, errResp.connectorErrorWire)
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

func (c *SocketClient) doJSONStream(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader) //nolint:gosec // unix-socket transport, not a real HTTP destination
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set(APIHeaderName, APIVersion)
	req.Header.Set(ProgressHeaderName, "1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req) //nolint:gosec // see NewRequestWithContext note
	if err != nil {
		return c.classifyTransportError(method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if readErr != nil {
			return fmt.Errorf("read response: %w", readErr)
		}
		var errResp ErrorResponse
		if jerr := json.Unmarshal(data, &errResp); jerr == nil && errResp.Error != "" {
			return daemonError(errResp.Error, errResp.connectorErrorWire)
		}
		return fmt.Errorf("daemon: HTTP %d: %s", resp.StatusCode, string(data))
	}

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 10<<20))
	scanner.Buffer(make([]byte, 0, 64*1024), 10<<20)
	var gotResult bool
	for scanner.Scan() {
		var env StreamEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			return fmt.Errorf("decode stream envelope: %w", err)
		}
		switch env.Type {
		case "notification":
			if env.Notification != nil {
				gmcp.Notify(ctx, *env.Notification)
			}
		case "result":
			gotResult = true
			if out != nil && len(env.Result) > 0 {
				if err := json.Unmarshal(env.Result, out); err != nil {
					return fmt.Errorf("decode stream result: %w", err)
				}
			}
		case "error":
			if env.Error == "" {
				env.Error = "unknown daemon error"
			}
			return daemonError(env.Error, env.connectorErrorWire)
		default:
			return fmt.Errorf("daemon: unknown stream envelope %q", env.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read response stream: %w", err)
	}
	if !gotResult {
		return fmt.Errorf("daemon: stream ended without result")
	}
	return nil
}

// classifyTransportError turns an http.Client.Do error into either a
// DaemonUnreachableError or a plain transport error.
//
// The distinction that matters is NOT "does this look like the daemon is
// down". It is "can we prove the request was never delivered", because
// DaemonUnreachableError is what licenses a caller to retry the operation
// in-process. A dial failure is safe: nothing was ever written. An EOF, a
// connection reset or a timeout is not, because the daemon may have
// executed the request before the connection died — which is precisely
// what deploying the daemon does to an in-flight mutation.
func (c *SocketClient) classifyTransportError(method, path string, err error) error {
	if requestNeverSent(err) {
		c.logger.Warn("client.socket.dial_failed",
			"path", c.dialPath,
			"error", err.Error())
		return &DaemonUnreachableError{Path: c.dialPath, Err: err}
	}
	// Delivery is unproven, so this error must not reach a caller that
	// reads it as permission to re-run the operation somewhere else.
	c.logger.Warn("client.socket.request_failed",
		"path", c.dialPath,
		"method", method,
		"route", path,
		"error", err.Error())
	return fmt.Errorf("daemon request failed after the request was sent; it may have been executed: %w", err)
}

// requestNeverSent reports whether err proves the connection was never
// established, and therefore that not a byte of the request reached the
// daemon. Anything it cannot prove, it calls delivered.
func requestNeverSent(err error) bool {
	if err == nil {
		return false
	}
	// A failed dial — refused, missing socket, or dial timeout — is the
	// one case where Go tells us no connection ever existed. Any other
	// Op ("read", "write") means we were already talking to the daemon.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "dial"
	}
	// Defensive: a bare syscall error, not wrapped in *net.OpError, can
	// only have come from establishing the connection.
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}

// DaemonUnreachableError is returned by SocketClient when the daemon
// socket could not be dialed — meaning the request was never delivered.
// Callers use errors.As both to format a structured "daemon not running"
// error (`cerberus mcp`) and to decide that falling back to in-process
// execution is safe. Because it carries that second meaning, it is
// returned only when non-delivery is proven; see classifyTransportError.
type DaemonUnreachableError struct {
	Path string
	Err  error
}

func (e *DaemonUnreachableError) Error() string {
	return fmt.Sprintf("cerberus daemon not running at %s; start with 'cerberus daemon' (%v)", e.Path, e.Err)
}

func (e *DaemonUnreachableError) Unwrap() error { return e.Err }
