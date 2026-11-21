package opssink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/opsref"
)

func outcomeRec(op, code string) audit.Record {
	return audit.Record{
		Kind: audit.KindOutcome, OperationID: "op123", Connector: "local", Operation: op,
		OutcomeCode: code, Decision: audit.DecisionAllowed,
		Target: audit.Target{Kind: "resource", Fields: map[string]string{"id": "tether-hub"}, Resource: "tether-hub"},
		OpsID:  "ops-system-of-record", WorkstreamID: "workstream:01M7Z8Q1TYGJRP0X85Z7Z88N90", Task: "CW-20261121-0155",
		Revision: "263eff6a1b2c", Health: "http://127.0.0.1:8991/healthz 200",
	}
}

func TestBuildGoldenPayload(t *testing.T) {
	post, ok := Build(outcomeRec(OpDeploy, "ok"))
	if !ok {
		t.Fatal("no post for a healthy deploy")
	}
	got, _ := json.Marshal(post)
	want := `{"ops_id":"ops-system-of-record","workstream_id":"workstream:01M7Z8Q1TYGJRP0X85Z7Z88N90","task":"CW-20261121-0155",` +
		`"state":"done","kind":"deployed","summary":"tether-hub 263eff6a1b2c deployed, health http://127.0.0.1:8991/healthz 200",` +
		`"evidence":[{"kind":"cerberus.deploy","ref":"op123"},{"kind":"commit","ref":"263eff6a1b2c"},{"kind":"health","ref":"http://127.0.0.1:8991/healthz 200"}],` +
		`"idempotency_key":"cerberus-deploy-op123-deployed"}`
	if string(got) != want {
		t.Fatalf("payload\n got %s\nwant %s", got, want)
	}
}

func TestOutcomeMapping(t *testing.T) {
	cases := []struct {
		rec         audit.Record
		kind, state string
		summary     string
	}{
		{outcomeRec(OpDeploy, "ok"), "deployed", "done", "tether-hub 263eff6a1b2c deployed, health "},
		{outcomeRec(OpDeploy, "operation_failed"), "deploy_failed", "blocked", "tether-hub 263eff6a1b2c deploy failed, health "},
		{func() audit.Record {
			r := outcomeRec(OpDeployRecord, "ok")
			r.DeployOutcome = OutcomeRolledBack
			return r
		}(), "rolled_back", "note", "tether-hub 263eff6a1b2c rolled back, health "},
		{func() audit.Record {
			r := outcomeRec(OpDeployRecord, "ok")
			r.DeployOutcome = OutcomeDeployFailed
			return r
		}(), "deploy_failed", "blocked", "deploy failed"},
		{func() audit.Record {
			r := outcomeRec(OpDeployRecord, "ok")
			r.DeployOutcome = OutcomeDeployed
			return r
		}(), "deployed", "done", "deployed"},
	}
	for _, c := range cases {
		p, ok := Build(c.rec)
		if !ok || p.Kind != c.kind || p.State != c.state || !strings.Contains(p.Summary, strings.TrimSuffix(c.summary, " ")) {
			t.Errorf("%s/%s: got %+v ok=%v, want kind %s state %s summary ~%q", c.rec.Operation, c.rec.DeployOutcome, p, ok, c.kind, c.state, c.summary)
		}
	}
}

func TestNotAnOutcome(t *testing.T) {
	intent := outcomeRec(OpDeploy, "ok")
	intent.Kind = audit.KindIntent
	dry := outcomeRec(OpDeploy, "ok")
	dry.DryRun = true
	refused := outcomeRec(OpDeploy, "ack_required")
	refused.Decision = audit.DecisionRefused
	other := outcomeRec("apply", "ok")
	badRecord := outcomeRec(OpDeployRecord, "ok")
	badRecord.DeployOutcome = "bogus"
	for name, r := range map[string]audit.Record{"intent": intent, "dry run": dry, "refused": refused, "apply": other, "bad outcome": badRecord} {
		if _, ok := Build(r); ok {
			t.Errorf("%s produced a post", name)
		}
	}
}

func TestIdempotencyKeyStable(t *testing.T) {
	a, _ := Build(outcomeRec(OpDeploy, "ok"))
	b, _ := Build(outcomeRec(OpDeploy, "ok"))
	if a.IdempotencyKey != b.IdempotencyKey || a.IdempotencyKey != "cerberus-deploy-op123-deployed" {
		t.Fatalf("keys %q %q", a.IdempotencyKey, b.IdempotencyKey)
	}
	if IdempotencyKey("op123", OutcomeRolledBack) == a.IdempotencyKey {
		t.Fatal("the kind must be part of the key")
	}
}

func TestUnlinkedPostOmitsIDs(t *testing.T) {
	r := outcomeRec(OpDeploy, "ok")
	r.OpsID, r.WorkstreamID, r.Task, r.Health, r.Revision = "", "", "", "", ""
	p, ok := Build(r)
	if !ok {
		t.Fatal("an unlinked deploy must still be posted")
	}
	raw, _ := json.Marshal(p)
	for _, k := range []string{"ops_id", "workstream_id", `"task"`} {
		if strings.Contains(string(raw), k) {
			t.Errorf("%s present in %s", k, raw)
		}
	}
}

// fakeOps is an Ops endpoint that records what it was sent.
type fakeOps struct {
	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header
	handler func(w http.ResponseWriter, r *http.Request, n int)
	calls   atomic.Int32
}

func (f *fakeOps) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.calls.Add(1))
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
		if f.handler != nil {
			f.handler(w, r, n)
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSink(t *testing.T, endpoint string, retries int, logs io.Writer) *Sink {
	tok := filepath.Join(t.TempDir(), "operator.token")
	if err := os.WriteFile(tok, []byte("s3cret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(opsref.Settings{Enabled: true, Endpoint: endpoint, TokenFile: tok, Retries: &retries, TimeoutSeconds: 1},
		slog.New(slog.NewTextHandler(logs, nil)))
	s.backoff = func(int) time.Duration { return time.Millisecond }
	return s
}

func wait(s *Sink) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.Wait(ctx)
}

func TestTransportShape(t *testing.T) {
	f := &fakeOps{}
	srv := f.server(t)
	s := newSink(t, srv.URL, 0, io.Discard)
	s.Observe(outcomeRec(OpDeploy, "ok"))
	wait(s)
	if len(f.bodies) != 1 {
		t.Fatalf("%d posts", len(f.bodies))
	}
	h := f.headers[0]
	if h.Get("Authorization") != "Bearer s3cret-token" || h.Get("X-Actor") != "msg://agent/agent-mux/agt_azos2rzing" || h.Get("X-Task-Id") != "CW-20261121-0155" {
		t.Fatalf("headers %v", h)
	}
	b := f.bodies[0]
	params := b["params"].(map[string]any)
	if b["method"] != "tools/call" || params["name"] != "status_post" {
		t.Fatalf("body %v", b)
	}
	args := params["arguments"].(map[string]any)
	if args["idempotency_key"] != "cerberus-deploy-op123-deployed" || args["kind"] != "deployed" || args["task"] != "CW-20261121-0155" {
		t.Fatalf("arguments %v", args)
	}
}

func TestNoTaskHeaderWhenUnknown(t *testing.T) {
	f := &fakeOps{}
	srv := f.server(t)
	s := newSink(t, srv.URL, 0, io.Discard)
	r := outcomeRec(OpDeploy, "ok")
	r.Task = ""
	s.Observe(r)
	wait(s)
	if len(f.headers) != 1 || f.headers[0].Get("X-Task-Id") != "" {
		t.Fatalf("headers %v", f.headers)
	}
}

// One post per outcome, none for an intermediate record, through the audit sink.
func TestWriterPostsOncePerOutcome(t *testing.T) {
	f := &fakeOps{}
	srv := f.server(t)
	s := newSink(t, srv.URL, 0, io.Discard)
	w := Wrap(audit.NewMemory(), s)

	intent := outcomeRec(OpDeploy, "")
	intent.Kind = audit.KindIntent
	intent.Decision = ""
	if _, err := w.Write(intent); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(outcomeRec("apply", "ok")); err != nil {
		t.Fatal(err)
	}
	wait(s)
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("%d posts for an intent and an apply, want 0", n)
	}
	if _, err := w.Write(outcomeRec(OpDeploy, "ok")); err != nil {
		t.Fatal(err)
	}
	wait(s)
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("%d posts for one deploy outcome, want 1", n)
	}
}

type failingSink struct{ audit.Sink }

func (failingSink) Write(audit.Record) (audit.Record, error) {
	return audit.Record{}, errors.New("disk full")
}

func TestNoPostWhenTheRecordWasNotWritten(t *testing.T) {
	f := &fakeOps{}
	srv := f.server(t)
	s := newSink(t, srv.URL, 0, io.Discard)
	w := Wrap(failingSink{audit.NewMemory()}, s)
	if _, err := w.Write(outcomeRec(OpDeploy, "ok")); err == nil {
		t.Fatal("the audit error must come through")
	}
	wait(s)
	if f.calls.Load() != 0 {
		t.Fatal("posted for a record that was not written")
	}
}

func TestNeverFailsTheDeploy(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter, r *http.Request, n int){
		"refusal": func(w http.ResponseWriter, _ *http.Request, _ int) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown argument ops_id"}}`))
		},
		"tool error": func(w http.ResponseWriter, _ *http.Request, _ int) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"forbidden"}]}}`))
		},
		"http 500": func(w http.ResponseWriter, _ *http.Request, _ int) { http.Error(w, "boom", 500) },
		"garbage":  func(w http.ResponseWriter, _ *http.Request, _ int) { _, _ = w.Write([]byte("<html>")) },
		"timeout":  func(w http.ResponseWriter, r *http.Request, _ int) { <-r.Context().Done() },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeOps{handler: h}
			srv := f.server(t)
			var logs bytes.Buffer
			s := newSink(t, srv.URL, 1, &logs)
			s.client.Timeout = 100 * time.Millisecond
			s.cfg.TimeoutSeconds = 1
			mem := audit.NewMemory()
			w := Wrap(mem, s)
			start := time.Now()
			out, err := w.Write(outcomeRec(OpDeploy, "ok"))
			if err != nil || out.OperationID != "op123" {
				t.Fatalf("the audit write was affected: %v", err)
			}
			if time.Since(start) > 50*time.Millisecond {
				t.Fatalf("Write blocked for %v", time.Since(start))
			}
			wait(s)
			if !strings.Contains(logs.String(), "ops_sink.post_failed") {
				t.Fatalf("failure not logged: %s", logs.String())
			}
			if strings.Contains(logs.String(), "s3cret-token") {
				t.Fatal("the token was logged")
			}
		})
	}
}

func TestRefusalLogsOpsCodeAndIsNotRetried(t *testing.T) {
	f := &fakeOps{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid params"}}`))
	}}
	srv := f.server(t)
	var logs bytes.Buffer
	s := newSink(t, srv.URL, 3, &logs)
	s.Observe(outcomeRec(OpDeploy, "ok"))
	wait(s)
	if f.calls.Load() != 1 {
		t.Fatalf("a refusal was retried: %d calls", f.calls.Load())
	}
	if !strings.Contains(logs.String(), "-32602") {
		t.Fatalf("the Ops error code was not logged: %s", logs.String())
	}
}

func TestTransientFailureIsRetriedWithTheSameKey(t *testing.T) {
	f := &fakeOps{handler: func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}}
	srv := f.server(t)
	s := newSink(t, srv.URL, 2, io.Discard)
	s.Observe(outcomeRec(OpDeploy, "ok"))
	wait(s)
	if f.calls.Load() != 2 {
		t.Fatalf("%d calls, want 2", f.calls.Load())
	}
	key := func(i int) any {
		return f.bodies[i]["params"].(map[string]any)["arguments"].(map[string]any)["idempotency_key"]
	}
	if key(0) != key(1) {
		t.Fatal("a retry changed the idempotency key")
	}
}

func TestEventStreamResponse(t *testing.T) {
	f := &fakeOps{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32001,\"message\":\"nope\"}}\n\n"))
	}}
	srv := f.server(t)
	var logs bytes.Buffer
	s := newSink(t, srv.URL, 0, &logs)
	s.Observe(outcomeRec(OpDeploy, "ok"))
	wait(s)
	if !strings.Contains(logs.String(), "-32001") {
		t.Fatalf("an event-stream refusal was not read: %s", logs.String())
	}
}

func TestMissingTokenFileIsLoggedNotFatal(t *testing.T) {
	var logs bytes.Buffer
	zero := 0
	s := New(opsref.Settings{Enabled: true, Endpoint: "http://127.0.0.1:1/mcp", TokenFile: filepath.Join(t.TempDir(), "absent"), Retries: &zero}, slog.New(slog.NewTextHandler(&logs, nil)))
	s.Observe(outcomeRec(OpDeploy, "ok"))
	wait(s)
	if !strings.Contains(logs.String(), "ops_sink.post_failed") {
		t.Fatalf("not logged: %s", logs.String())
	}
}

// A negative retries setting must send exactly once and never panic.
func TestNegativeRetriesSendsOnce(t *testing.T) {
	f := &fakeOps{handler: func(w http.ResponseWriter, _ *http.Request, _ int) { http.Error(w, "boom", 500) }}
	srv := f.server(t)
	var logs bytes.Buffer
	s := newSink(t, srv.URL, -1, &logs)
	s.Observe(outcomeRec(OpDeploy, "ok"))
	wait(s)
	if f.calls.Load() != 1 {
		t.Fatalf("%d attempts, want 1", f.calls.Load())
	}
	// Even a sink built around unclamped settings must not panic.
	s.cfg.Retries = nil
	s.send(Post{Kind: "deployed"})
}
