package cerbapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pausectl"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/standby"
)

// socketRoutes are the patterns routes() registers, read from its source so a
// new route joins the table below without anyone remembering to add it.
func socketRoutes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("socket_server.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		out = append(out, m[1])
	}
	if len(out) < 25 {
		t.Fatalf("found only %d socket routes; the pattern no longer matches routes()", len(out))
	}
	return out
}

// requestPaths turns a route pattern into the concrete paths a caller sends.
func requestPaths(route string) []string {
	switch route {
	case "/resources/":
		var out []string
		for _, id := range []string{"flagged", "other"} {
			for _, action := range []string{"", "status", "doctor", "inspect", "logs",
				"apply", "stop", "reload", "deploy", "sync", "remove",
				"apply/plan", "stop/plan", "reload/plan", "apply/confirm", "stop/confirm", "reload/break-glass", "stop/break-glass"} {
				p := "/resources/" + id
				if action != "" {
					p += "/" + action
				}
				out = append(out, p)
			}
		}
		return out
	}
	if strings.HasSuffix(route, "/") {
		return []string{route + "x", route + "x/y"}
	}
	return []string{route}
}

// The fence's rule is exact over every socket route and method: GET and HEAD
// pass (bar a resource's logs, which are gated and recorded), POST of apply,
// stop or reload to the flagged resource itself runs on the standby path, and
// everything else is refused.
func TestStandbyAllowsExactlyReadsAndTheFlaggedVerbs(t *testing.T) {
	methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	checked := 0
	for _, route := range socketRoutes(t) {
		for _, path := range requestPaths(route) {
			for _, method := range methods {
				got, op := standbyClassify(method, path, "flagged")
				want, wantOp := standbyRefuse, ""
				switch {
				case (method == http.MethodGet || method == http.MethodHead) && strings.HasSuffix(path, "/logs") && strings.HasPrefix(path, "/resources/"):
				case method == http.MethodGet || method == http.MethodHead:
					want = standbyRead
				case method == http.MethodPost && path == "/resources/flagged/apply":
					want, wantOp = standbyVerb, "apply"
				case method == http.MethodPost && path == "/resources/flagged/stop":
					want, wantOp = standbyVerb, "stop"
				case method == http.MethodPost && path == "/resources/flagged/reload":
					want, wantOp = standbyVerb, "reload"
				}
				if got != want || op != wantOp {
					t.Errorf("%s %s: decision %d %q, want %d %q", method, path, got, op, want, wantOp)
				}
				checked++
			}
		}
	}
	// With no resource named, no verb runs at all.
	for _, verb := range []string{"apply", "stop", "reload"} {
		if got, _ := standbyClassify(http.MethodPost, "/resources/flagged/"+verb, ""); got != standbyRefuse {
			t.Errorf("a flag naming no resource allowed %s", verb)
		}
	}
	t.Logf("%d route, path and method combinations checked", checked)
}

// standbyHome is a private HOME holding a config with two dev sessions, and
// the main audit chain anchored in an in-memory keyring.
type standbyHome struct {
	home, state string
	runtime     *ResourceRuntimeService
	mainDir     string
	anchor      *memAnchor
	fence       *Standby
	paths       standby.Paths
}

type memAnchor struct {
	mu     sync.Mutex
	head   audit.Head
	ok     bool
	stores int
}

func (m *memAnchor) Load() (audit.Head, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.head, m.ok, nil
}

func (m *memAnchor) Store(h audit.Head) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.head, m.ok = h, true
	m.stores++
	return nil
}

func newStandbyHome(t *testing.T) *standbyHome {
	t.Helper()
	base := t.TempDir()
	h := &standbyHome{home: filepath.Join(base, "home")}
	h.state = filepath.Join(h.home, ".cerberus")
	t.Setenv("HOME", h.home)
	t.Setenv("TMPDIR", filepath.Join(base, "tmp"))
	for _, d := range []string{h.state, filepath.Join(base, "tmp"), filepath.Join(base, "work"), filepath.Join(h.state, "approvals")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fmt.Sprintf(`version: 2
resources:
  - id: flagged
    type: process
    connector: local
    env: dev
    owner: self
    config:
      mode: dev_session
      command: ["/bin/sleep", "3600"]
      dir: %[1]s
  - id: other
    type: process
    connector: local
    env: dev
    owner: self
    config:
      mode: dev_session
      command: ["/bin/sleep", "3601"]
      dir: %[1]s
`, filepath.Join(base, "work"))
	cfgPath := filepath.Join(h.state, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.state, "approvals", "events.jsonl"), []byte(`{"seq":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.mainDir = filepath.Join(h.state, "audit")
	h.anchor = &memAnchor{}
	audit.SetAnchor(h.mainDir, h.anchor)
	t.Cleanup(func() { audit.SetAnchor(h.mainDir, nil) })
	main, err := audit.OpenFileSink(h.mainDir)
	if err != nil {
		t.Fatal(err)
	}
	h.runtime = NewResourceRuntimeService(main, WithResourceRuntimeConfigPath(cfgPath))
	h.paths = standby.PathsIn(h.state)
	h.fence = NewStandby(h.runtime, h.paths.AuditDir(), nil)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range []string{"flagged", "other"} {
			if res, ok := h.runtime.ResourceDef(id); ok {
				_ = h.runtime.localConnector().Stop(ctx, resourceDefToDomain(res))
			}
		}
	})
	return h
}

func (h *standbyHome) serve(t *testing.T) *SocketClient {
	t.Helper()
	sockPath := filepath.Join("/tmp", fmt.Sprintf("cs-%d-%d.sock", os.Getpid(), time.Now().UnixNano())) // short: a unix socket path is limited to 108 bytes
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	srv := NewSocketServer(NewInProcessClient(WithResourceRuntimeService(h.runtime)), sockPath, WithStandby(h.fence))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("socket server: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if conn, err := net.Dial("unix", sockPath); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return NewSocketClient(sockPath)
}

// tree fingerprints every file under root that the check covers: content and
// mode. skip says which relative paths are not covered.
func tree(t *testing.T, root string, skip func(rel string) bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			out[rel] = info.Mode().String()
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // the test's own tree
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[rel] = fmt.Sprintf("%s %d %s %d", info.Mode(), info.Size(), hex.EncodeToString(sum[:]), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diffTrees(before, after map[string]string) []string {
	var d []string
	for k, v := range before {
		if after[k] != v {
			d = append(d, "changed or removed: "+k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			d = append(d, "added: "+k)
		}
	}
	return d
}

// standbyCovered skips what the check does not cover: the standby directory
// (standby's own record) and the dev session runtime files of the flagged
// resource (its pid, meta and lock, which starting and stopping it necessarily
// write, as systemd's own files would for a unit).
func standbyCovered(rel string) bool {
	switch {
	case rel == standby.DirName, strings.HasPrefix(rel, standby.DirName+string(filepath.Separator)):
		return true
	case strings.HasPrefix(rel, "pids"+string(filepath.Separator)+"flagged."),
		strings.HasPrefix(rel, "locks"+string(filepath.Separator)+"flagged."):
		return true
	}
	return false
}

// In standby the flagged resource's start, reload and stop run over the real
// socket, without the gate, and are recorded only in the standby chain: the
// main chain, its anchor, approvals/events.jsonl and every other file under
// ~/.cerberus stay byte-identical, and neither paused.<id> nor
// runtime/applied.json is written.
func TestStandbyVerbsWriteOnlyTheStandbyChain(t *testing.T) {
	h := newStandbyHome(t)
	client := h.serve(t)
	ctx := context.Background()

	// Normal operation first, so the main chain and applied.json exist.
	if out, err := client.ApplyResource(ctx, "other", WithAcknowledged(true)); err != nil || !out.Success {
		t.Fatalf("apply other before standby: %+v %v", out, err)
	}
	// A policy that would hold every lifecycle operation for approval: the
	// standby path must not consult it.
	withPDP(t, constantPDP{decision: policy.Approve})

	h.fence.Set(standby.State{Active: true, Resource: "flagged"})
	anchorBefore, _, _ := h.anchor.Load()
	storesBefore := h.anchor.stores
	before := tree(t, h.state, standbyCovered)

	for _, step := range []struct {
		name string
		run  func() (*OpResult, error)
	}{
		{"apply", func() (*OpResult, error) { return client.ApplyResource(ctx, "flagged", WithAcknowledged(true)) }},
		{"reload", func() (*OpResult, error) { return client.ReloadResource(ctx, "flagged", WithAcknowledged(true)) }},
		{"stop", func() (*OpResult, error) { return client.StopResource(ctx, "flagged", WithAcknowledged(true)) }},
		{"apply again", func() (*OpResult, error) { return client.ApplyResource(ctx, "flagged", WithAcknowledged(true)) }},
	} {
		out, err := step.run()
		if err != nil || out == nil || !out.Success {
			t.Fatalf("%s of the flagged resource in standby: %+v %v", step.name, out, err)
		}
	}
	// Reads are served, and marked.
	st, err := client.GetResourceRuntime(ctx, "flagged")
	if err != nil || st == nil {
		t.Fatalf("status in standby: %v", err)
	}
	if _, err = client.ListResources(ctx, ResourceListArgs{}); err != nil {
		t.Fatalf("list in standby: %v", err)
	}
	// Everything else is refused before it runs.
	if _, err = client.StopResource(ctx, "other", WithAcknowledged(true)); err == nil {
		t.Fatal("stop of another resource ran in standby")
	}
	if _, err = client.ResourceLogs(ctx, "flagged", 10, ""); err == nil {
		t.Fatal("logs, which write the main chain, were served in standby")
	}

	if d := diffTrees(before, tree(t, h.state, standbyCovered)); len(d) > 0 {
		t.Fatalf("standby wrote under ~/.cerberus outside the standby chain:\n%s", strings.Join(d, "\n"))
	}
	if anchorAfter, _, _ := h.anchor.Load(); anchorAfter != anchorBefore || h.anchor.stores != storesBefore {
		t.Fatalf("the main chain's anchor moved: %+v -> %+v (%d stores)", anchorBefore, anchorAfter, h.anchor.stores-storesBefore)
	}
	if pausectl.IsServicePaused("flagged") {
		t.Fatal("a standby stop wrote paused.flagged")
	}

	// The standby chain is a chain of its own and verifies: genesis, then an
	// intent and an outcome per verb, recorded as standby acting.
	checked, err := audit.Check(h.paths.AuditDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(checked.Problems) > 0 {
		t.Fatalf("standby chain does not verify: %v", checked.Problems)
	}
	var outcomes []string
	for _, rec := range checked.Records {
		if rec.Kind == audit.KindOutcome {
			if rec.Principal.Via != "standby" || rec.OutcomeCode != audit.OutcomeOK {
				t.Errorf("standby record: %+v", rec)
			}
			outcomes = append(outcomes, rec.Operation)
		}
	}
	if strings.Join(outcomes, ",") != "apply,reload,stop,apply" {
		t.Fatalf("standby chain outcomes = %v", outcomes)
	}
	if _, err := os.Stat(filepath.Join(h.paths.AuditDir(), ".digest_key")); err != nil {
		t.Fatalf("standby chain has no digest key of its own: %v", err)
	}
}

// A refusal reaches the operator over the socket as the guidance it was,
// naming the new CLI and how to leave standby; so does one for a flag that
// names no resource.
func TestStandbyRefusalReachesTheCallerIntact(t *testing.T) {
	h := newStandbyHome(t)
	client := h.serve(t)
	ctx := context.Background()
	for name, st := range map[string]standby.State{
		"flagged":  {Active: true, Resource: "flagged"},
		"no name":  {Active: true, Problem: "the flag file names no resource"},
		"bad name": {Active: true, Problem: `the flag file's first line "../x" is not a resource id`},
	} {
		h.fence.Set(st)
		_, err := client.StopResource(ctx, "other", WithAcknowledged(true))
		if err == nil {
			t.Fatalf("%s: refused nothing", name)
		}
		text := redact.ErrorText(err)
		for _, want := range []string{"is in standby", "new `cerberus` CLI", "remove ~/.cerberus/standby", "POST /resources/other/stop"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: refusal %q lacks %q", name, text, want)
			}
		}
		if st.Problem != "" && !strings.Contains(text, st.Problem) {
			t.Errorf("%s: refusal %q does not say why no resource is allowed", name, text)
		}
	}
}

// Reads in standby say so: the header on every answer, and "standby": true in
// a JSON object; the daemon status keeps its own standby object.
func TestStandbyMarksReads(t *testing.T) {
	h := newStandbyHome(t)
	client := h.serve(t)
	ctx := context.Background()
	h.fence.Set(standby.State{Active: true, Resource: "flagged"})

	st, err := client.PingStatus(ctx)
	if err != nil || st.Standby == nil || st.Standby.Resource != "flagged" || st.Standby.Since == "" {
		t.Fatalf("daemon status in standby: %+v %v", st.Standby, err)
	}
	raw, err := client.doRaw(ctx, http.MethodGet, "/resources/flagged/status")
	if err != nil {
		t.Fatal(err)
	}
	if raw.header.Get(StandbyHeader) != "flagged" || !strings.Contains(string(raw.body), `"standby": true`) {
		t.Fatalf("read not marked: header %q body %s", raw.header.Get(StandbyHeader), raw.body)
	}

	h.fence.Set(standby.State{})
	st, err = client.PingStatus(ctx)
	if err != nil || st.Standby != nil {
		t.Fatalf("daemon status out of standby: %+v %v", st.Standby, err)
	}
	raw, err = client.doRaw(ctx, http.MethodGet, "/resources/flagged/status")
	if err != nil || raw.header.Get(StandbyHeader) != "" || strings.Contains(string(raw.body), `"standby"`) {
		t.Fatalf("a read out of standby is marked: %v %s", err, raw.body)
	}
}

// Dev sessions stop on the standby entry path without a pause flag, are
// listed, and start again from the list.
func TestStandbyStopsAndRestartsDevSessions(t *testing.T) {
	h := newStandbyHome(t)
	ctx := context.Background()
	for _, id := range []string{"flagged", "other"} {
		if out, err := h.runtime.ApplyResource(BeginRequest(ctx, SurfaceInProcess), id, WithAcknowledged(true)); err != nil || !out.Success {
			t.Fatalf("apply %s: %+v %v", id, out, err)
		}
	}
	stopped, err := h.runtime.StopDevSessions(ctx)
	if err != nil || strings.Join(stopped, ",") != "flagged,other" {
		t.Fatalf("stopped = %v, %v", stopped, err)
	}
	for _, id := range stopped {
		if pausectl.IsServicePaused(id) {
			t.Fatalf("%s was paused", id)
		}
		if running(t, h, id) {
			t.Fatalf("%s still running", id)
		}
	}
	if again, err := h.runtime.StopDevSessions(ctx); err != nil || len(again) != 0 {
		t.Fatalf("a second stop listed %v, %v", again, err)
	}
	if err := h.runtime.StartDevSessions(ctx, stopped); err != nil {
		t.Fatal(err)
	}
	for _, id := range stopped {
		if !running(t, h, id) {
			t.Fatalf("%s not started again", id)
		}
	}
	if err := h.runtime.StartDevSessions(ctx, []string{"gone"}); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("an unknown id was not named: %v", err)
	}
}

func running(t *testing.T, h *standbyHome, id string) bool {
	t.Helper()
	res, ok := h.runtime.ResourceDef(id)
	if !ok {
		t.Fatalf("no resource %s", id)
	}
	state, err := h.runtime.localConnector().Status(context.Background(), resourceDefToDomain(res))
	if err != nil {
		t.Fatal(err)
	}
	return !resourceStateDown(state)
}

type rawResponse struct {
	header http.Header
	body   []byte
}

// doRaw sends one request through the client's socket transport and returns
// the answer as it came, header included.
func (c *SocketClient) doRaw(ctx context.Context, method, path string) (rawResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return rawResponse{}, err
	}
	req.Header.Set(APIHeaderName, APIVersion)
	resp, err := c.http.Do(req)
	if err != nil {
		return rawResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return rawResponse{header: resp.Header, body: body}, err
}
