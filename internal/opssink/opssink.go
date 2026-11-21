// Package opssink posts a deploy's outcome to the Ops service as one
// ops_status_post (design caster://ops/design.system-of-record-v2@2, 5.2/5.3).
//
// One post per deploy OUTCOME, never per intermediate state: deployed, or
// deploy_failed, or rolled_back. A deploy with no known id is posted anyway;
// the Ops service files it as unlinked. Posting is best effort: it runs off the
// caller's path, is bounded by a timeout and a retry count, and never fails or
// delays a deploy. Cerberus writes no Torque comment itself; Ops mirrors them.
package opssink

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/opsref"
)

// Outcomes of a deploy (the status kinds of design 5.2).
const (
	OutcomeDeployed     = "deployed"
	OutcomeDeployFailed = "deploy_failed"
	OutcomeRolledBack   = "rolled_back"
)

// Operations whose outcome records are deploy outcomes.
const (
	OpDeploy       = "deploy"
	OpDeployRecord = "deploy_record"
)

// states are the ops_status_post state for each outcome.
var states = map[string]string{
	OutcomeDeployed:     "done",
	OutcomeDeployFailed: "blocked",
	OutcomeRolledBack:   "note",
}

// ValidOutcome reports whether s is a deploy outcome.
func ValidOutcome(s string) bool { _, ok := states[s]; return ok }

// Evidence is one evidence reference of a post.
type Evidence struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// Post is the ops_status_post arguments, flat and top-level as in design 5.3.
// ops_id, workstream_id and task are left out when empty.
type Post struct {
	OpsID          string     `json:"ops_id,omitempty"`
	WorkstreamID   string     `json:"workstream_id,omitempty"`
	Task           string     `json:"task,omitempty"`
	State          string     `json:"state"`
	Kind           string     `json:"kind"`
	Summary        string     `json:"summary"`
	Evidence       []Evidence `json:"evidence"`
	IdempotencyKey string     `json:"idempotency_key"`
}

var commitLike = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// OutcomeOf is the deploy outcome an audit record reports, and whether it
// reports one. Only an outcome that ran counts: an intent, a refused call and a
// dry run say nothing about a deploy.
func OutcomeOf(rec audit.Record) (string, bool) {
	if rec.Kind != audit.KindOutcome || rec.Connector != "local" || rec.DryRun || rec.Decision != audit.DecisionAllowed || rec.OperationID == "" {
		return "", false
	}
	switch rec.Operation {
	case OpDeploy:
		if rec.OutcomeCode == audit.OutcomeOK {
			return OutcomeDeployed, true
		}
		return OutcomeDeployFailed, true
	case OpDeployRecord:
		return rec.DeployOutcome, ValidOutcome(rec.DeployOutcome)
	}
	return "", false
}

// Build is the post for a deploy outcome record, or false when the record is
// not one.
func Build(rec audit.Record) (Post, bool) {
	outcome, ok := OutcomeOf(rec)
	if !ok {
		return Post{}, false
	}
	resource := rec.Target.Fields["id"]
	if resource == "" {
		resource = rec.Target.Resource
	}
	verb := map[string]string{OutcomeDeployed: "deployed", OutcomeDeployFailed: "deploy failed", OutcomeRolledBack: "rolled back"}[outcome]
	health := rec.Health
	if health == "" {
		health = "unknown"
	}
	parts := []string{resource}
	if rec.Revision != "" {
		parts = append(parts, rec.Revision)
	}
	summary := fmt.Sprintf("%s %s, health %s", strings.Join(parts, " "), verb, health)

	ev := []Evidence{{Kind: "cerberus.deploy", Ref: rec.OperationID}}
	if rec.Revision != "" {
		kind := "revision"
		if commitLike.MatchString(rec.Revision) {
			kind = "commit"
		}
		ev = append(ev, Evidence{Kind: kind, Ref: rec.Revision})
	}
	if rec.Health != "" {
		ev = append(ev, Evidence{Kind: "health", Ref: rec.Health})
	}
	return Post{
		OpsID: rec.OpsID, WorkstreamID: rec.WorkstreamID, Task: rec.Task,
		State: states[outcome], Kind: outcome, Summary: summary, Evidence: ev,
		IdempotencyKey: IdempotencyKey(rec.OperationID, outcome),
	}, true
}

// IdempotencyKey is cerberus-deploy-<operation id>-<kind>: stable for one
// outcome of one operation, so a retry or a replay is deduplicated by Ops.
func IdempotencyKey(operationID, kind string) string {
	return "cerberus-deploy-" + operationID + "-" + kind
}

// Sink posts deploy outcomes.
type Sink struct {
	cfg    opsref.Settings
	client *http.Client
	logger *slog.Logger
	// backoff is the wait before retry n (1-based); tests shorten it.
	backoff func(n int) time.Duration

	wg sync.WaitGroup
}

// New builds a sink from settings (defaults filled). It posts nothing unless
// asked through Observe; callers construct it only when ops.enabled.
func New(cfg opsref.Settings, logger *slog.Logger) *Sink {
	cfg = cfg.WithDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	return &Sink{
		cfg: cfg, logger: logger,
		client:  &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second},
		backoff: func(n int) time.Duration { return time.Duration(n) * 500 * time.Millisecond },
	}
}

// Observe posts the record's deploy outcome in the background. It returns at
// once and never fails: a record that is not a deploy outcome is ignored.
func (s *Sink) Observe(rec audit.Record) {
	post, ok := Build(rec)
	if !ok {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.send(post)
	}()
}

// Wait blocks until the posts in flight finish or ctx ends. A short-lived
// process (the CLI) calls it before exiting so its post is not cut off.
func (s *Sink) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// send posts with bounded retries. A refusal from Ops is final (retrying the
// same post cannot change it); a transport error or a 5xx is retried.
func (s *Sink) send(post Post) {
	attempts := 1
	if s.cfg.Retries != nil && *s.cfg.Retries > 0 {
		attempts += *s.cfg.Retries
	}
	last := errors.New("no attempt was made")
	for i := 1; i <= attempts; i++ {
		retry, err := s.postOnce(post)
		if err == nil {
			s.logger.Info("ops_sink.posted", "kind", post.Kind, "idempotency_key", post.IdempotencyKey)
			return
		}
		last = err
		if !retry || i == attempts {
			break
		}
		time.Sleep(s.backoff(i))
	}
	s.logger.Warn("ops_sink.post_failed", "kind", post.Kind, "idempotency_key", post.IdempotencyKey, "error", last.Error())
}

type rpcResponse struct {
	Error *struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result *struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

// postOnce makes one attempt. retry says whether another attempt could help.
func (s *Sink) postOnce(post Post) (retry bool, err error) {
	token, rerr := os.ReadFile(s.cfg.TokenFile) //nolint:gosec // the operator's configured token file
	if rerr != nil {
		return false, fmt.Errorf("read the ops token file: %w", pathless(rerr))
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": s.cfg.Tool, "arguments": post},
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoint, bytes.NewReader(body))
	if rerr != nil {
		return false, fmt.Errorf("build the ops request: %w", rerr)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("X-Actor", s.cfg.Actor)
	if post.Task != "" {
		req.Header.Set("X-Task-Id", post.Task)
	}
	resp, derr := s.client.Do(req)
	if derr != nil {
		// Never include the request: its header holds the token.
		return true, fmt.Errorf("post to ops: %w", stripURL(derr))
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return true, fmt.Errorf("ops answered HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode >= 300 && len(raw) == 0 {
		return false, fmt.Errorf("ops refused the post: HTTP %d", resp.StatusCode)
	}
	var rpc rpcResponse
	if jerr := json.Unmarshal(jsonPayload(raw), &rpc); jerr != nil {
		if resp.StatusCode >= 300 {
			return false, fmt.Errorf("ops refused the post: HTTP %d", resp.StatusCode)
		}
		return false, fmt.Errorf("ops answered something that is not JSON-RPC (HTTP %d)", resp.StatusCode)
	}
	switch {
	case rpc.Error != nil:
		return false, fmt.Errorf("ops refused the post: code %v: %s", rpc.Error.Code, rpc.Error.Message)
	case rpc.Result != nil && rpc.Result.IsError:
		msg := ""
		if len(rpc.Result.Content) > 0 {
			msg = rpc.Result.Content[0].Text
		}
		return false, fmt.Errorf("ops refused the post: %s", msg)
	case resp.StatusCode >= 300:
		return false, fmt.Errorf("ops refused the post: HTTP %d", resp.StatusCode)
	}
	return false, nil
}

// jsonPayload unwraps an event-stream body (the last data: line) and leaves a
// plain JSON body as it is.
func jsonPayload(raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return trimmed
	}
	var last []byte
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if line := sc.Bytes(); bytes.HasPrefix(line, []byte("data:")) {
			last = bytes.TrimSpace(line[5:])
		}
	}
	if last == nil {
		return trimmed
	}
	return last
}

func pathless(err error) error {
	if pe, ok := err.(*os.PathError); ok { //nolint:errorlint
		return pe.Err
	}
	return err
}

func stripURL(err error) error {
	if ue, ok := err.(interface{ Unwrap() error }); ok {
		if inner := ue.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}

// Writer wraps an audit sink: each deploy outcome record that is written
// successfully is handed to the ops sink. The record is written first and its
// result returned unchanged, so posting can never change what the audit log
// does.
type Writer struct {
	audit.Sink
	ops *Sink
}

// Wrap returns inner with deploy outcomes observed by ops.
func Wrap(inner audit.Sink, ops *Sink) audit.Sink {
	if ops == nil {
		return inner
	}
	return &Writer{Sink: inner, ops: ops}
}

// Write implements audit.Sink.
func (w *Writer) Write(rec audit.Record) (audit.Record, error) {
	out, err := w.Sink.Write(rec)
	if err == nil {
		w.ops.Observe(out)
	}
	return out, err
}
