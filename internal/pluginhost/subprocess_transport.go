package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

const defaultProcessCloseTimeout = 3 * time.Second

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

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
		decoder:      json.NewDecoder(stdout),
		closeTimeout: closeTimeout,
		pending:      make(map[int64]chan callResult),
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
	decoder      *json.Decoder
	closeTimeout time.Duration

	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[int64]chan callResult

	nextID    int64
	closed    atomic.Bool
	exitErr   error
	exitErrMu sync.RWMutex
	exited    chan struct{}
	failOnce  sync.Once
}

var _ Process = (*RPCProcess)(nil)

func (p *RPCProcess) Init(ctx context.Context, params SDKInitParams) (SDKInitResult, error) {
	var result SDKInitResult
	if err := p.call(ctx, SDKMethodInit, params, &result); err != nil {
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

func (p *RPCProcess) Close() error {
	if p.closed.Swap(true) {
		return p.exitError()
	}
	_ = p.stdin.Close()

	select {
	case <-p.exited:
		return p.exitError()
	case <-time.After(p.closeTimeout):
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		<-p.exited
		return p.exitError()
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

	id := atomic.AddInt64(&p.nextID, 1)
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
		var resp rpcResponse
		if err := p.decoder.Decode(&resp); err != nil {
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

func (p *RPCProcess) deletePending(id int64) {
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
