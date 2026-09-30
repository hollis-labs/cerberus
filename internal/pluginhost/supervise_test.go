package pluginhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The test binary doubles as the plugin shim and as a family of fake
// plugins, chosen by CERB_FAKE_PLUGIN, each misbehaving in one way.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == PluginExecCommand {
		err := RunPluginExec(os.Args[2:], os.Environ())
		fmt.Fprintln(os.Stderr, err)
		os.Exit(127)
	}
	if mode := os.Getenv("CERB_FAKE_PLUGIN"); mode != "" {
		fakePluginMain(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakePluginMain(mode string) {
	dir := os.Getenv("CERB_FAKE_DIR")
	_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600) //nolint:gosec // the test's own temp dir
	var writeMu sync.Mutex
	reply := func(id int64, result any) {
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		writeMu.Lock()
		_, _ = os.Stdout.Write(append(data, '\n'))
		writeMu.Unlock()
	}
	content := func(v any) map[string]any {
		raw, _ := json.Marshal(v)
		return map[string]any{"content": json.RawMessage(raw)}
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for in.Scan() {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				ToolName string `json:"tool_name"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			continue
		}
		go func() {
			switch req.Method {
			case SDKMethodInit:
				if mode == "hang-init" {
					return
				}
				if mode == "fork" {
					child := exec.Command("/bin/sleep", "300")
					_ = child.Start()
					_ = os.WriteFile(filepath.Join(dir, "child.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0o600) //nolint:gosec // the test's own temp dir
				}
				reply(req.ID, map[string]any{"id": "docker", "version": "dev", "protocol": SDKProtocolVersion})
			case SDKMethodLoad:
				reply(req.ID, map[string]any{})
				if mode == "exit" {
					time.Sleep(50 * time.Millisecond)
					os.Exit(3)
				}
			case SDKMethodHealth:
				if mode == "hang-health" {
					return
				}
				reply(req.ID, map[string]any{"ok": true})
			case SDKMethodUnload:
				reply(req.ID, map[string]any{})
			case SDKMethodMCPCallTool:
				switch mode {
				case "hang-call":
					return
				case "flood":
					writeMu.Lock()
					chunk := bytes.Repeat([]byte("x"), 1<<20)
					for i := 0; i < 20; i++ {
						_, _ = os.Stdout.Write(chunk)
					}
					writeMu.Unlock()
					return
				case "big-text":
					reply(req.ID, content(strings.Repeat("y", 2<<20)))
				case "big-struct":
					reply(req.ID, content(map[string]any{"blob": strings.Repeat("z", 2<<20)}))
				case "stderr-flood":
					line := strings.Repeat("e", 99) + "\n"
					for i := 0; i < 2000; i++ {
						_, _ = os.Stderr.WriteString(line)
					}
					reply(req.ID, content(map[string]any{"ok": true}))
				case "rlimits":
					var nofile, fsize, core syscall.Rlimit
					_ = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &nofile)
					_ = syscall.Getrlimit(syscall.RLIMIT_FSIZE, &fsize)
					_ = syscall.Getrlimit(syscall.RLIMIT_CORE, &core)
					reply(req.ID, content(map[string]any{"nofile": nofile.Cur, "fsize": fsize.Cur, "core": core.Cur, "path": os.Getenv("PATH")}))
				default:
					reply(req.ID, content(map[string]any{"ok": true}))
				}
			}
		}()
	}
}

// syncBuffer is a goroutine-safe stderr sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type fakeHost struct {
	m      *Manager
	dirs   map[string]string
	stderr *syncBuffer
	mu     sync.Mutex
	events []RestartEvent
}

func (h *fakeHost) observed(kind string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, e := range h.events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// newFakeHost is a manager over real subprocesses: one plugin per id, each
// running mode, with limits from a connector-config the test writes.
func newFakeHost(t *testing.T, shim bool, plugins map[string]string, limits map[string]*LimitSettings) *fakeHost {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHost{dirs: map[string]string{}, stderr: &syncBuffer{}}
	cfg := ConnectorConfig{Entries: map[string]PluginSettings{}}
	for id, l := range limits {
		cfg.Entries[id] = PluginSettings{Limits: l}
	}
	launchers := map[string]SubprocessLauncher{}
	for id, mode := range plugins {
		dir := t.TempDir()
		h.dirs[id] = dir
		// The plugin runs under launchd's minimal PATH, as the daemon does.
		launchers[id] = SubprocessLauncher{
			Transport: StdioTransportFactory{Stderr: h.stderr, CloseTimeout: 500 * time.Millisecond},
			Env:       []string{"PATH=/usr/bin:/bin", "CERB_FAKE_PLUGIN=" + mode, "CERB_FAKE_DIR=" + dir},
		}
		if shim {
			l := launchers[id]
			l.Shim = exe
			launchers[id] = l
		}
		script := "#!/bin/sh\nexec " + strconv.Quote(exe) + "\n"
		writeScript(t, dir, "bin/plugin", script)
	}
	h.m = NewManager(nil, perPluginLauncher(launchers), "test",
		WithConnectorConfig(func() (ConnectorConfig, error) { return cfg, nil }),
		WithRestartObserver(func(ev RestartEvent) { h.mu.Lock(); h.events = append(h.events, ev); h.mu.Unlock() }))
	h.m.restartDelays = []time.Duration{10 * time.Millisecond}
	for id := range plugins {
		p := validInstalledPlugin()
		p.ID, p.Path = id, h.dirs[id]
		p.Spec = testPluginSpec(Entrypoint{Command: "bin/plugin"})
		p.Spec.ID = id
		p.Manifest.ID = id
		p.Manifest.Operations = []contract.ManifestOperation{
			{Name: "read_it", Effect: contract.EffectRead, InputSchema: contract.ObjectSchema(map[string]any{})},
			{Name: "write_it", Effect: contract.EffectWrite, InputSchema: contract.ObjectSchema(map[string]any{})},
		}
		p.Spec.Cerberus.Connector = p.Manifest
		h.m.RegisterInstalled(p)
	}
	t.Cleanup(func() {
		for id := range plugins {
			h.m.sup.mu.Lock()
			h.m.sup.held[id] = true
			h.m.sup.mu.Unlock()
			_ = h.m.Unload(context.Background(), id)
		}
	})
	return h
}

type perPluginLauncher map[string]SubprocessLauncher

func (p perPluginLauncher) Launch(ctx context.Context, plugin InstalledPlugin) (Process, error) {
	return p[plugin.ID].Launch(ctx, plugin)
}

func writeScript(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // an executable test script
		t.Fatal(err)
	}
}

func pidIn(t *testing.T, path string) int {
	t.Helper()
	for i := 0; i < 100; i++ {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 { //nolint:gosec // the test's own temp dir
			n, _ := strconv.Atoi(string(data))
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

func gone(pid int) bool {
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 250; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A plugin that hangs in Init misses its deadline and is killed, and while
// it hangs another plugin's calls and health go on: no lock is held across
// a plugin's own calls.
func TestHungInitIsBoundedAndHoldsNothingUp(t *testing.T) {
	h := newFakeHost(t, false, map[string]string{"slow": "hang-init", "good": "ok"},
		map[string]*LimitSettings{"slow": {InitTimeout: 700 * time.Millisecond}})
	if err := h.m.Load(context.Background(), "good"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- h.m.Load(context.Background(), "slow") }()
	time.Sleep(100 * time.Millisecond)
	callStart := time.Now()
	if _, err := h.m.ExecuteOperation(context.Background(), OperationArgs{Connector: "good", Operation: "read_it", Config: map[string]any{}}); err != nil {
		t.Fatalf("a call to another plugin: %v", err)
	}
	if health, err := h.m.Health(context.Background(), "good"); err != nil || !health.Healthy {
		t.Fatalf("another plugin's health: %+v %v", health, err)
	}
	if took := time.Since(callStart); took > 400*time.Millisecond {
		t.Fatalf("another plugin waited %s behind a hung Init", took)
	}
	err := <-done
	var deadline *DeadlineError
	if !errors.As(err, &deadline) || deadline.Phase != "init" || time.Since(start) > 3*time.Second {
		t.Fatalf("hung init: %v after %s", err, time.Since(start))
	}
	if !gone(pidIn(t, filepath.Join(h.dirs["slow"], "pid"))) {
		t.Fatal("the hung plugin's process survived")
	}
}

// A call past its deadline answers deadline_exceeded saying a write may
// have partly run, stops the plugin with its group, and restarts it; after
// three restarts in the window the host gives up, loudly.
func TestCallDeadlineStopsRestartsAndGivesUp(t *testing.T) {
	h := newFakeHost(t, false, map[string]string{"hang": "hang-call"},
		map[string]*LimitSettings{"hang": {CallTimeout: 300 * time.Millisecond}})
	if err := h.m.Load(context.Background(), "hang"); err != nil {
		t.Fatal(err)
	}
	firstPID := pidIn(t, filepath.Join(h.dirs["hang"], "pid"))
	_, err := h.m.ExecuteOperation(context.Background(), OperationArgs{Connector: "hang", Operation: "write_it", Config: map[string]any{}, Acknowledged: true})
	var deadline *DeadlineError
	if !errors.As(err, &deadline) || !strings.Contains(err.Error(), "may have partly run") || !errors.Is(err, ErrDeadlineExceeded) {
		t.Fatalf("a hung call: %v", err)
	}
	if !gone(firstPID) {
		t.Fatal("the hung plugin's process survived")
	}
	for i := 1; i <= maxRestarts; i++ {
		waitFor(t, fmt.Sprintf("restart %d", i), func() bool { return h.observed("restarted") >= i && h.m.Loaded("hang") })
		_, _ = h.m.ExecuteOperation(context.Background(), OperationArgs{Connector: "hang", Operation: "read_it", Config: map[string]any{}})
	}
	waitFor(t, "the give-up", func() bool { return h.observed("gave_up") == 1 })
	if h.m.Loaded("hang") || h.m.GaveUp("hang") == "" {
		t.Fatal("the plugin was not left unloaded with the reason")
	}
	if health, _ := h.m.Health(context.Background(), "hang"); !strings.Contains(health.Message, "not restarted after 3 restarts") {
		t.Fatalf("health after the give-up: %+v", health)
	}
	// A read's timeout does not say it may have run.
	if !strings.Contains((&DeadlineError{Connector: "x", Phase: "call", Operation: "read_it", Timeout: time.Second, Effect: contract.EffectRead}).Error(), "restarting") {
		t.Fatal("a read's deadline message")
	}
	// An operator's load clears the give-up.
	if err = h.m.Load(context.Background(), "hang"); err != nil || h.m.GaveUp("hang") != "" {
		t.Fatalf("load after the give-up: %v", err)
	}
}

// A hung health check is a missed deadline too: unhealthy, stopped, and
// restarted.
func TestHungHealthIsBounded(t *testing.T) {
	h := newFakeHost(t, false, map[string]string{"sick": "hang-health"},
		map[string]*LimitSettings{"sick": {HealthTimeout: 300 * time.Millisecond}})
	if err := h.m.Load(context.Background(), "sick"); err != nil {
		t.Fatal(err)
	}
	health, err := h.m.Health(context.Background(), "sick")
	if err != nil || health.Healthy || !strings.Contains(health.Message, "did not answer health within 300ms") {
		t.Fatalf("hung health: %+v %v", health, err)
	}
	waitFor(t, "the restart", func() bool { return h.observed("restarted") == 1 })
}

// Every process the plugin forked dies with it.
func TestForkedChildrenDieWithThePlugin(t *testing.T) {
	h := newFakeHost(t, false, map[string]string{"forker": "fork"}, nil)
	if err := h.m.Load(context.Background(), "forker"); err != nil {
		t.Fatal(err)
	}
	child := pidIn(t, filepath.Join(h.dirs["forker"], "child.pid"))
	if err := h.m.Unload(context.Background(), "forker"); err != nil {
		t.Fatal(err)
	}
	if !gone(child) {
		t.Fatal("the plugin's child outlived it")
	}
}

// A plugin that floods stdout costs the host at most one message's worth:
// a read is refused as output_too_large, a write answers that it may have
// run, and either way the plugin is stopped and restarted.
func TestStdoutFloodIsBounded(t *testing.T) {
	h := newFakeHost(t, false, map[string]string{"flood": "flood", "flood2": "flood"}, nil)
	for _, id := range []string{"flood", "flood2"} {
		if err := h.m.Load(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	_, err := h.m.ExecuteOperation(context.Background(), OperationArgs{Connector: "flood", Operation: "read_it", Config: map[string]any{}})
	var tooLarge *OutputTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("a flooded read: %v", err)
	}
	out, err := h.m.ExecuteOperation(context.Background(), OperationArgs{Connector: "flood2", Operation: "write_it", Config: map[string]any{}, Acknowledged: true})
	if err != nil || out.OutputCap == nil || out.OutputCap.Action != "unreadable" || !strings.Contains(fmt.Sprint(out.Data), "whether it succeeded is unknown") {
		t.Fatalf("a flooded write: %+v %v", out, err)
	}
	waitFor(t, "both restarts", func() bool { return h.observed("restarted") >= 2 })
}

// readMessage stops at its cap on an endless line: it never reads the
// flood into memory.
func TestReadMessageStopsAtItsCap(t *testing.T) {
	r := bufio.NewReaderSize(endless{}, 4096)
	if _, err := readMessage(r, 1<<20); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("an endless line: %v", err)
	}
	line, err := readMessage(bufio.NewReader(strings.NewReader("{\"a\":1}\n{\"b\":2}\n")), 1<<20)
	if err != nil || string(line) != "{\"a\":1}\n" {
		t.Fatalf("a framed message: %q %v", line, err)
	}
}

type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// Over-cap text is truncated with a marker for every effect; over-cap
// structured content is refused for a read and withheld with a note for a
// write, which already ran.
func TestResultCaps(t *testing.T) {
	h := newFakeHost(t, false, map[string]string{"text": "big-text", "struct": "big-struct"}, nil)
	for _, id := range []string{"text", "struct"} {
		if err := h.m.Load(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	out, err := h.m.ExecuteOperation(context.Background(), OperationArgs{Connector: "text", Operation: "write_it", Config: map[string]any{}, Acknowledged: true})
	text, _ := out.Data.(string)
	if err != nil || len(text) > (1<<20)+200 || !strings.Contains(text, "[cerberus: output truncated — 2.0 MiB returned, 1.0 MiB shown]") || out.OutputCap.Action != "truncated" {
		t.Fatalf("over-cap text: %d bytes, %v", len(text), err)
	}
	_, err = h.m.ExecuteOperation(context.Background(), OperationArgs{Connector: "struct", Operation: "read_it", Config: map[string]any{}})
	var tooLarge *OutputTooLargeError
	if !errors.As(err, &tooLarge) || !strings.Contains(err.Error(), "max_result_bytes") {
		t.Fatalf("an over-cap structured read: %v", err)
	}
	out, err = h.m.ExecuteOperation(context.Background(), OperationArgs{Connector: "struct", Operation: "write_it", Config: map[string]any{}, Acknowledged: true})
	if err != nil || out.OutputCap == nil || out.OutputCap.Action != "withheld" || !strings.Contains(fmt.Sprint(out.Data), "write_it ran and succeeded") {
		t.Fatalf("an over-cap structured write: %+v %v", out, err)
	}
}

// A plugin's stderr reaches the daemon log within its budget, and the
// excess is counted by a marker line.
func TestStderrBudget(t *testing.T) {
	var next bytes.Buffer
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	tap := newStderrTap()
	tap.now = func() time.Time { return now }
	tap.setPlugin("noisy")
	_, _ = tap.wrap(&next).Write(bytes.Repeat([]byte(strings.Repeat("e", 99)+"\n"), 2000))
	if next.Len() > stderrBudget {
		t.Fatalf("%d bytes reached the log, over the %d budget", next.Len(), stderrBudget)
	}
	now = now.Add(time.Minute)
	_, _ = tap.Write([]byte("later\n"))
	if !strings.Contains(next.String(), "[cerberus: plugin noisy stderr: ") || !strings.Contains(next.String(), "lines dropped in the last minute") || !strings.HasSuffix(next.String(), "later\n") {
		t.Fatalf("no marker:\n%s", next.String()[max(0, next.Len()-300):])
	}
}

// Through the shim, a plugin starts under launchd's minimal PATH with its
// rlimits set and no core dumps.
func TestShimSetsRlimitsUnderAMinimalPath(t *testing.T) {
	h := newFakeHost(t, true, map[string]string{"limited": "rlimits"},
		map[string]*LimitSettings{"limited": {OpenFiles: 256, FileMiB: 64}})
	if err := h.m.Load(context.Background(), "limited"); err != nil {
		t.Fatal(err)
	}
	out, err := h.m.ExecuteOperation(context.Background(), OperationArgs{Connector: "limited", Operation: "read_it", Config: map[string]any{}})
	got, _ := out.Data.(map[string]any)
	if err != nil || got["nofile"] != float64(256) || got["fsize"] != float64(64<<20) || got["core"] != float64(0) || got["path"] != "/usr/bin:/bin" {
		t.Fatalf("limits in the plugin: %v %v", got, err)
	}
}

// A plugin that exits on its own is restarted.
func TestExitedPluginIsRestarted(t *testing.T) {
	h := newFakeHost(t, false, map[string]string{"quitter": "exit"}, nil)
	if err := h.m.Load(context.Background(), "quitter"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a restart", func() bool { return h.observed("restarted") >= 1 })
	h.mu.Lock()
	reason := h.events[0].Reason
	h.mu.Unlock()
	if !strings.Contains(reason, "exited on its own") {
		t.Fatalf("reason %q", reason)
	}
}

// The memory watchdog stops a plugin whose group is over its limit.
func TestMemoryWatchdog(t *testing.T) {
	h := newFakeHost(t, false, map[string]string{"fat": "ok"}, map[string]*LimitSettings{"fat": {MemoryMiB: 1}})
	h.m.memoryInterval = 20 * time.Millisecond
	h.m.rssFn = func(int) (int64, error) { return 10 << 20, nil }
	if err := h.m.Load(context.Background(), "fat"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the watchdog", func() bool { return h.observed("stopped") >= 1 })
	h.mu.Lock()
	reason := h.events[0].Reason
	h.mu.Unlock()
	if !strings.Contains(reason, "resident memory, 10.0 MiB, went over its 1.0 MiB limit") {
		t.Fatalf("reason %q", reason)
	}
	if rss, err := groupRSS(os.Getpid()); err != nil || rss < 0 {
		t.Fatalf("groupRSS: %d %v", rss, err)
	}
}

// A plugin loaded under a request's context outlives the request.
func TestLoadContextDoesNotBindTheProcess(t *testing.T) {
	h := newFakeHost(t, false, map[string]string{"p": "ok"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := h.m.Load(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(200 * time.Millisecond)
	if health, err := h.m.Health(context.Background(), "p"); err != nil || !health.Healthy {
		t.Fatalf("after the load's context ended: %+v %v", health, err)
	}
}

// Limits clamp to the host maximums, with a warning.
func TestClampLimits(t *testing.T) {
	l, warnings := ClampLimits(&LimitSettings{CallTimeout: 2 * time.Hour, Operations: map[string]time.Duration{"deploy": 20 * time.Minute}, OpenFiles: 1 << 20})
	if l.Call != MaxLimits.Call || l.CallTimeout("deploy") != 20*time.Minute || l.CallTimeout("other") != MaxLimits.Call || l.Process.OpenFiles != MaxLimits.Process.OpenFiles || len(warnings) != 2 {
		t.Fatalf("%+v %v", l, warnings)
	}
	if d, _ := ClampLimits(nil); d.Call != DefaultLimits.Call || d.Process.CPUSeconds != 0 {
		t.Fatalf("defaults %+v", d)
	}
}
