package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/dataaccess"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	pluginsdk "github.com/hollis-labs/cerberus/pkg/plugin"
)

// queryProcess is the one-shot instance a data query starts. Its Init and
// its call can each be held, so a test controls where the request goroutine
// is while the cancel watcher runs.
type queryProcess struct {
	initResult  SDKInitResult
	initEntered chan struct{} // closed when Init is reached; nil: Init does not wait
	initRelease chan struct{}
	callEntered chan struct{} // closed when CallTool is first reached
	callOnce    sync.Once
	blockCall   bool // CallTool waits for its context
}

func (p *queryProcess) Init(ctx context.Context, _ SDKInitParams) (SDKInitResult, error) {
	if p.initRelease != nil {
		close(p.initEntered)
		select {
		case <-p.initRelease:
		case <-ctx.Done():
			return SDKInitResult{}, ctx.Err()
		}
	}
	return p.initResult, nil
}
func (p *queryProcess) Load(context.Context) (SDKLoadResult, error) { return SDKLoadResult{}, nil }
func (p *queryProcess) Unload(context.Context) error                { return nil }
func (p *queryProcess) Health(context.Context) (SDKHealthResult, error) {
	return SDKHealthResult{OK: true}, nil
}
func (p *queryProcess) CallTool(ctx context.Context, _ SDKMCPCallRequest) (SDKMCPCallResult, error) {
	p.callOnce.Do(func() { close(p.callEntered) })
	if p.blockCall {
		<-ctx.Done()
		return SDKMCPCallResult{}, ctx.Err()
	}
	return SDKMCPCallResult{Content: []byte(`{"rows":[]}`)}, nil
}
func (p *queryProcess) Close() error { return nil }

// parentThenQueryLauncher hands the first launch (the plugin's own load) the
// parent process and every later launch (the data query's one-shot
// instance) the query process, so the two never share a fake.
type parentThenQueryLauncher struct {
	mu       sync.Mutex
	launches int
	parent   Process
	query    Process
}

func (l *parentThenQueryLauncher) Launch(context.Context, InstalledPlugin) (Process, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.launches++
	if l.launches == 1 {
		return l.parent, nil
	}
	return l.query, nil
}

func dataQueryPlugin() InstalledPlugin {
	plugin := validInstalledPlugin()
	plugin.Spec.Capabilities = []CapabilityRequest{{Name: pluginsdk.DataQueryCapability}}
	// An open schema: the data query's selectors pass the key table.
	plugin.Manifest.Operations = []contract.ManifestOperation{
		{Name: "query", Effect: contract.EffectRead, InputSchema: map[string]any{"type": "object"}},
	}
	return plugin
}

// loadDataQueryPlugin installs and loads a data_query_v1 plugin whose later
// one-shot instance is query.
func loadDataQueryPlugin(t *testing.T, query *queryProcess) (*Manager, InstalledPlugin) {
	t.Helper()
	plugin := dataQueryPlugin()
	initResult := SDKInitResult{CapabilityContract: 1, ID: plugin.ID, Version: plugin.Version, Protocol: SDKProtocolVersion}
	query.initResult = initResult
	parent := &fakeProcess{initResult: initResult}
	manager := newTestManager(t, nil, &parentThenQueryLauncher{parent: parent, query: query}, "test")
	manager.RegisterInstalled(plugin)
	if err := manager.Load(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return manager, plugin
}

// dataQueryRequest is a query and a ctx carrying the session's proof for it,
// with a permit that ExecuteOperation accepts.
func dataQueryRequest(ctx context.Context, t *testing.T, connector string) (context.Context, OperationArgs) {
	t.Helper()
	args := OperationArgs{
		Connector: connector,
		Operation: "query",
		Config: map[string]any{
			"target":     "orders",
			"projection": "summary",
			"version":    "1",
			"parameters": map[string]any{"limit": 5},
		},
	}
	raw, err := json.Marshal(args.Config)
	if err != nil {
		t.Fatal(err)
	}
	var query pluginsdk.DataQuery
	if err := json.Unmarshal(raw, &query); err != nil {
		t.Fatal(err)
	}
	permit := pluginsdk.DataPermit{
		Digest:    pluginsdk.DataQueryDigest(query),
		ExpiresAt: time.Now().Add(1500 * time.Millisecond),
		MaxRows:   10,
		MaxBytes:  1 << 10,
	}
	if err := permit.Validate(query, time.Now()); err != nil {
		t.Fatalf("test permit: %v", err)
	}
	return dataaccess.WithProof(ctx, dataaccess.Proof{Connector: connector, Query: query, Permit: permit}), args
}

type operationOutcome struct {
	result OperationResult
	err    error
}

// The data-query branch's cancel watcher must not read lp, which the request
// goroutine reassigns to the one-shot instance once it has started
// (CW-20261115-0002). The race is only observable when the watcher runs
// late: one scheduled early reads lp and then calls queryCtx.Done(), and the
// request goroutine's later Done() on the same context (context.WithTimeout
// in start) orders that read before `lp = inst`. GOMAXPROCS(1) and an Init
// that does not wait keep the watcher off the CPU until the request
// goroutine has reassigned lp; under -race a watcher that reads lp is
// reported. A watcher is occasionally scheduled early anyway, so the query
// runs several times.
func TestDataQueryWatcherDoesNotReadReassignedInstance(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	query := &queryProcess{callEntered: make(chan struct{})}
	manager, plugin := loadDataQueryPlugin(t, query)

	for range 5 {
		ctx, args := dataQueryRequest(context.Background(), t, plugin.ID)
		outcome := make(chan operationOutcome, 1)
		go func() {
			result, err := manager.ExecuteOperation(ctx, args)
			outcome <- operationOutcome{result, err}
		}()

		select {
		case got := <-outcome:
			if got.err != nil {
				t.Fatalf("ExecuteOperation: %v", got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("ExecuteOperation did not return")
		}
		// The receive above put this goroutine ahead of the still-queued
		// watcher; parking lets it run while the test is still running, so
		// a race is reported against this test rather than the package.
		time.Sleep(20 * time.Millisecond)
	}
	if err := manager.Unload(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Unload: %v", err)
	}
}

// Unloading the parent cancels an in-flight data query. The watcher must
// wait on the parent's done even when it is first scheduled after the
// request goroutine has moved lp to the one-shot instance: GOMAXPROCS(1)
// and an Init that does not wait keep it off the CPU until the call blocks.
func TestDataQueryUnloadingParentCancelsInFlightQuery(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	query := &queryProcess{callEntered: make(chan struct{}), blockCall: true}
	manager, plugin := loadDataQueryPlugin(t, query)

	outer, cancelOuter := context.WithCancel(context.Background())
	defer cancelOuter()
	ctx, args := dataQueryRequest(outer, t, plugin.ID)
	outcome := make(chan operationOutcome, 1)
	go func() {
		result, err := manager.ExecuteOperation(ctx, args)
		outcome <- operationOutcome{result, err}
	}()

	select {
	case <-query.callEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the one-shot instance's call was never reached")
	}
	if err := manager.Unload(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Unload: %v", err)
	}

	select {
	case got := <-outcome:
		var stopped *StoppedError
		if got.err == nil {
			t.Fatalf("ExecuteOperation succeeded after its plugin was unloaded: %+v", got.result)
		}
		if !errors.Is(got.err, context.Canceled) && !errors.As(got.err, &stopped) {
			t.Fatalf("ExecuteOperation error = %v, want a cancel or stopped error", got.err)
		}
	case <-time.After(5 * time.Second):
		cancelOuter() // release the held call so the goroutine ends
		<-outcome
		t.Fatal("unloading the parent did not cancel the in-flight data query within 5s")
	}
}
