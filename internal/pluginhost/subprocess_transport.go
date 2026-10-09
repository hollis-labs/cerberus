package pluginhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

// ErrMessageTooLarge is a plugin message over MaxMessageBytes. The stream
// cannot be resynchronized after one, so the plugin is stopped.
var ErrMessageTooLarge = errors.New("the plugin sent a message larger than the host reads")

const defaultProcessCloseTimeout = 3 * time.Second

// Use the SDK's tagged, safe-integer-aware IDs consistently for wire and
// pending-call correlation.
type rpcRequest = subprocess.RPCRequest
type rpcResponse = subprocess.RPCResponse

type callResult struct {
	response rpcResponse
	err      error
}

// StdioTransportFactory starts a plugin subprocess and exposes the
// plugin-sdk JSON-RPC protocol over stdin/stdout.
type StdioTransportFactory struct {
	Stderr       io.Writer
	CloseTimeout time.Duration
}

var _ TransportFactory = StdioTransportFactory{}

func (f StdioTransportFactory) Start(ctx context.Context, cmd *exec.Cmd) (Process, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin stdout: %w", err)
	}
	if cmd.Stderr == nil {
		if f.Stderr != nil {
			cmd.Stderr = f.Stderr
		} else {
			cmd.Stderr = io.Discard
		}
	}
	// A manager that correlates stderr with operations passes a tap in the
	// launch context. The tap forwards every byte to where it was going.
	if tap := stderrTapFrom(ctx); tap != nil {
		cmd.Stderr = tap.wrap(cmd.Stderr)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start plugin process: %w", err)
	}

	closeTimeout := f.CloseTimeout
	if closeTimeout <= 0 {
		closeTimeout = defaultProcessCloseTimeout
	}

	p := &RPCProcess{
		cmd:          cmd,
		stdin:        stdin,
		encoder:      json.NewEncoder(stdin),
		reader:       bufio.NewReaderSize(stdout, 64<<10),
		maxMessage:   MaxMessageBytes,
		closeTimeout: closeTimeout,
		pending:      make(map[subprocess.RPCID]chan callResult),
		exited:       make(chan struct{}),
	}
	go p.readResponses()
	go p.waitProcess()
	return p, nil
}

// RPCProcess is a protocol client for one running plugin-sdk subprocess.
type RPCProcess struct {
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	encoder      *json.Encoder
	reader       *bufio.Reader
	maxMessage   int
	closeTimeout time.Duration

	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[subprocess.RPCID]chan callResult

	nextID int64
	closed atomic.Bool
	// hostKilled is set when the host killed the process, so its exit
	// status is not reported as the plugin's failure.
	hostKilled atomic.Bool
	exitErr    error
	exitErrMu  sync.RWMutex
	exited     chan struct{}
	failOnce   sync.Once
}

var _ Process = (*RPCProcess)(nil)

func (p *RPCProcess) Init(ctx context.Context, params SDKInitParams) (SDKInitResult, error) {
	if err := params.Validate(); err != nil {
		return SDKInitResult{}, err
	}
	var result SDKInitResult
	if err := p.call(ctx, SDKMethodInit, params, &result); err != nil {
		return SDKInitResult{}, err
	}
	if err := subprocess.ValidateInitResult(params, result); err != nil {
		return SDKInitResult{}, err
	}
	return result, nil
}

func (p *RPCProcess) Load(ctx context.Context) (SDKLoadResult, error) {
	var result SDKLoadResult
	if err := p.call(ctx, SDKMethodLoad, struct{}{}, &result); err != nil {
		return SDKLoadResult{}, err
	}
	return result, nil
}

func (p *RPCProcess) Unload(ctx context.Context) error {
	return p.call(ctx, SDKMethodUnload, struct{}{}, nil)
}

func (p *RPCProcess) Health(ctx context.Context) (SDKHealthResult, error) {
	var result SDKHealthResult
	if err := p.call(ctx, SDKMethodHealth, struct{}{}, &result); err != nil {
		return SDKHealthResult{}, err
	}
	return result, nil
}

func (p *RPCProcess) CallTool(ctx context.Context, req SDKMCPCallRequest) (SDKMCPCallResult, error) {
	var result SDKMCPCallResult
	if err := p.call(ctx, SDKMethodMCPCallTool, req, &result); err != nil {
		return SDKMCPCallResult{}, err
	}
	return result, nil
}

// Command sends a plugin-sdk command/execute. The host sends one command
// only, a secret backend's resolve (plugin.ResolveCommand).
func (p *RPCProcess) Command(ctx context.Context, req SDKCommandRequest) (SDKCommandResult, error) {
	var result SDKCommandResult
	if err := p.call(ctx, SDKMethodCommandExecute, req, &result); err != nil {
		return SDKCommandResult{}, err
	}
	return result, nil
}

func (p *RPCProcess) Close() error {
	if p.closed.Swap(true) {
		return p.exitError()
	}
	_ = p.stdin.Close()

	select {
	case <-p.exited:
	case <-time.After(p.closeTimeout):
		// Past its grace period: the host stops it, which is not the
		// plugin's error to report.
		p.hostKilled.Store(true)
		p.killGroup()
		<-p.exited
	}
	// Whatever the plugin forked and left behind goes with it.
	p.killGroup()
	if p.hostKilled.Load() {
		return nil
	}
	return p.exitError()
}

// Kill stops the plugin and every process in its group now, and waits for
// it to be reaped.
func (p *RPCProcess) Kill() {
	p.closed.Store(true)
	p.hostKilled.Store(true)
	p.killGroup()
	<-p.exited
}

// Exited is closed once the plugin process has exited.
func (p *RPCProcess) Exited() <-chan struct{} { return p.exited }

// ProcessGroup is the plugin's process group id, which is its pid.
func (p *RPCProcess) ProcessGroup() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *RPCProcess) killGroup() {
	if p.cmd.Process == nil {
		return
	}
	// The group is the plugin's pid (Setpgid). A kill of the group reaches
	// forked children; one that moved to its own session or group escapes,
	// which macOS gives no way to prevent.
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (p *RPCProcess) call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.closed.Load() {
		if err := p.exitError(); err != nil {
			return err
		}
		return fmt.Errorf("plugin process is closed")
	}

	id := subprocess.NumberID(atomic.AddInt64(&p.nextID, 1))
	ch := make(chan callResult, 1)
	p.pendingMu.Lock()
	p.pending[id] = ch
	p.pendingMu.Unlock()

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	p.writeMu.Lock()
	err := p.encoder.Encode(req)
	p.writeMu.Unlock()
	if err != nil {
		p.deletePending(id)
		return fmt.Errorf("send %s: %w", method, err)
	}

	select {
	case res := <-ch:
		if res.err != nil {
			return res.err
		}
		if res.response.Error != nil {
			return res.response.Error
		}
		if out == nil || len(res.response.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(res.response.Result, out); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		p.deletePending(id)
		return ctx.Err()
	case <-p.exited:
		p.deletePending(id)
		if err := p.exitError(); err != nil {
			return err
		}
		return fmt.Errorf("plugin process exited")
	}
}

func (p *RPCProcess) readResponses() {
	for {
		line, err := readMessage(p.reader, p.maxMessage)
		if err != nil {
			if errors.Is(err, ErrMessageTooLarge) {
				// Unrecoverable: the rest of the stream is the same message.
				p.failAll(err)
				p.killGroup()
				return
			}
			p.failAll(fmt.Errorf("read plugin response: %w", err))
			return
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			p.failAll(fmt.Errorf("read plugin response: %w", err))
			return
		}

		p.pendingMu.Lock()
		ch, ok := p.pending[resp.ID]
		if ok {
			delete(p.pending, resp.ID)
		}
		p.pendingMu.Unlock()
		if ok {
			ch <- callResult{response: resp}
		}
	}
}

func (p *RPCProcess) waitProcess() {
	err := p.cmd.Wait()
	p.setExitError(err)
	p.failAll(p.exitError())
	close(p.exited)
}

func (p *RPCProcess) failAll(err error) {
	p.failOnce.Do(func() {
		p.pendingMu.Lock()
		defer p.pendingMu.Unlock()
		for id, ch := range p.pending {
			ch <- callResult{err: err}
			delete(p.pending, id)
		}
	})
}

func (p *RPCProcess) deletePending(id subprocess.RPCID) {
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	delete(p.pending, id)
}

func (p *RPCProcess) setExitError(err error) {
	p.exitErrMu.Lock()
	defer p.exitErrMu.Unlock()
	p.exitErr = err
}

func (p *RPCProcess) exitError() error {
	p.exitErrMu.RLock()
	defer p.exitErrMu.RUnlock()
	return p.exitErr
}

// readMessage reads one newline-framed message of at most max bytes. The
// SDK writes one JSON value per line. Past limit it stops reading into memory
// and reports ErrMessageTooLarge, so a flood costs the host max bytes, not
// the flood.
func readMessage(r *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > limit {
			return nil, fmt.Errorf("%w (%d bytes)", ErrMessageTooLarge, limit)
		}
		buf = append(buf, chunk...)
		switch {
		case err == nil:
			return buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(bytes.TrimSpace(buf)) > 0:
			return buf, nil
		default:
			return nil, err
		}
	}
}
