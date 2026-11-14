package cerbapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/standby"
)

// StandbyHeader carries the flagged resource on every socket response while
// the daemon is in standby.
const StandbyHeader = "Cerberus-Standby"

// Standby is the daemon's standby fence (docs/substrate-port-plan.md section
// 7.1). While it is active the socket serves reads, and status, apply, stop
// and reload of the one flagged resource; everything else is refused with
// guidance naming the new CLI. The allowed verbs run without the gate, write
// neither paused.<id> nor runtime/applied.json, and are recorded in the
// standby audit chain, never the main one.
type Standby struct {
	runtime  *ResourceRuntimeService
	auditDir string
	logger   *slog.Logger

	mu    sync.RWMutex
	state standby.State
	since time.Time

	sinkMu sync.Mutex
	sink   audit.Sink
}

// NewStandby builds the fence for runtime, keeping its audit chain in auditDir.
func NewStandby(runtime *ResourceRuntimeService, auditDir string, logger *slog.Logger) *Standby {
	if logger == nil {
		logger = slog.Default()
	}
	return &Standby{runtime: runtime, auditDir: auditDir, logger: logger}
}

// Set changes the fence's state.
func (s *Standby) Set(st standby.State) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.Active && !s.state.Active {
		s.since = time.Now()
	}
	s.state = st
}

// State is the fence's state, and since when it has been active.
func (s *Standby) State() (standby.State, time.Time) {
	if s == nil {
		return standby.State{}, time.Time{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state, s.since
}

// Sink is the standby audit chain, opened on first use: its own directory,
// genesis and digest key. The main chain never sees a standby record.
func (s *Standby) Sink() (audit.Sink, error) {
	s.sinkMu.Lock()
	defer s.sinkMu.Unlock()
	if s.sink != nil {
		return s.sink, nil
	}
	sink, err := audit.OpenFileSink(s.auditDir)
	if err != nil {
		return nil, fmt.Errorf("open the standby audit chain %s: %w", s.auditDir, err)
	}
	s.sink = sink
	return sink, nil
}

// standbyVerbs are the mutations allowed on the flagged resource, by the
// socket route's action. apply is how a resource starts.
var standbyVerbs = map[string]string{
	"apply":  localconn.OpApply,
	"stop":   localconn.OpStop,
	"reload": localconn.OpReload,
}

// standbyDecision is what the fence does with one request.
type standbyDecision int

const (
	standbyRefuse standbyDecision = iota
	standbyRead                   // pass to the normal handler, marked standby
	standbyVerb                   // run on the standby path
)

// standbyClassify decides one request against the flagged resource. It is
// exact: a read is any GET or HEAD except one that writes (a resource's logs
// are gated and recorded in the main chain); a verb is a POST of apply, stop
// or reload to the flagged resource itself, with no suffix.
func standbyClassify(method, path, flagged string) (standbyDecision, string) {
	if rest, ok := strings.CutPrefix(path, "/resources/"); ok {
		id, action, _ := strings.Cut(rest, "/")
		if (method == http.MethodGet || method == http.MethodHead) && action == "logs" {
			return standbyRefuse, ""
		}
		if method == http.MethodPost && flagged != "" && id == flagged {
			if op, ok := standbyVerbs[action]; ok {
				return standbyVerb, op
			}
		}
	}
	if method == http.MethodGet || method == http.MethodHead {
		return standbyRead, ""
	}
	return standbyRefuse, ""
}

// refusal is the guidance every refused request gets.
func (s *Standby) refusal(st standby.State, method, path string) error {
	allowed := "nothing but reads, because the flag names no resource (" + st.Problem + ")"
	if st.Resource != "" {
		allowed = fmt.Sprintf("reads, and status, apply, stop and reload of %q", st.Resource)
	}
	return redact.Guidance("this Cerberus is in standby, so it refused %s %s: the new Cerberus (the Substrate build) owns every write now. "+
		"Run the command with the new `cerberus` CLI instead. This daemon serves %s. "+
		"To leave standby, remove ~/.cerberus/standby", method, path, allowed)
}

// serve applies the fence to one socket request. It reports whether it
// answered; a read is left to the normal handler. stream answers a request
// that asked for a progress stream (SocketServer.handleStream), as the normal
// apply route does.
func (s *Standby) serve(w http.ResponseWriter, r *http.Request, stream func(http.ResponseWriter, *http.Request, func(context.Context) (interface{}, error)) bool) bool {
	st, _ := s.State()
	if !st.Active {
		return false
	}
	decision, op := standbyClassify(r.Method, r.URL.Path, st.Resource)
	switch decision {
	case standbyRead:
		return false
	case standbyVerb:
		s.serveVerb(w, r, st.Resource, op, stream)
		return true
	case standbyRefuse:
	}
	err := scopeError(redact.ScopeFrom(r.Context()), s.refusal(st, r.Method, r.URL.Path))
	s.logger.Warn("daemon.standby.refused", "method", r.Method, "path", r.URL.Path)
	writeServiceError(w, http.StatusConflict, err)
	return true
}

func (s *Standby) serveVerb(w http.ResponseWriter, r *http.Request, id, op string, stream func(http.ResponseWriter, *http.Request, func(context.Context) (interface{}, error)) bool) {
	opts, err := decodeMutationOptions(r.Body, routeRun)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if stream != nil && stream(w, r, func(ctx context.Context) (interface{}, error) { return s.Mutate(ctx, op, id, opts...) }) {
		return
	}
	out, err := s.Mutate(r.Context(), op, id, opts...)
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, scopeError(redact.ScopeFrom(r.Context()), err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Mutate runs apply, stop or reload of the flagged resource on the standby
// path: the operation's input contract and acknowledgment still hold, and the
// self-guard still refuses the daemon itself; policy, approvals and brakes are
// not consulted; nothing writes paused.<id> or runtime/applied.json; the
// intent and outcome go to the standby chain.
func (s *Standby) Mutate(ctx context.Context, op, id string, options ...MutationOption) (*OpResult, error) {
	st, _ := s.State()
	if !st.Active || st.Resource == "" || id != st.Resource {
		return nil, s.refusal(st, http.MethodPost, "/resources/"+id+"/"+op)
	}
	if ApplyMutationOptions(options).Plan {
		return nil, redact.Guidance("this Cerberus is in standby and runs %s of %q directly; it does not serve plans", op, id)
	}
	sink, err := s.Sink()
	if err != nil {
		return nil, redact.GuidanceWrap(err, "the standby audit chain could not be opened, so %s of %q was refused and nothing ran", op, id)
	}
	return s.runtime.standbyMutation(ctx, sink, op, id, options...)
}

// standbyMutation is Standby.Mutate's runtime half.
func (s *ResourceRuntimeService) standbyMutation(ctx context.Context, sink audit.Sink, op, id string, options ...MutationOption) (*OpResult, error) {
	if err := resourceGate(ctx, op, id, ApplyMutationOptions(options)); err != nil {
		return nil, err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	res, err := s.requireLocalProcessResource(ctx, id)
	if err != nil {
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	spec, err := localconn.SpecFromResourceConfig(res.Config)
	if err != nil {
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	if op != localconn.OpStop {
		if portErr := s.refusePortConflict(ctx, id); portErr != nil {
			return &OpResult{Success: false, ServiceID: id, Error: portErr.Error()}, nil
		}
	}
	if guardErr := s.refuseSelfMutation(res, spec); guardErr != nil {
		return &OpResult{Success: false, ServiceID: id, Error: guardErr.Error()}, nil
	}
	contractOp, _ := localconn.Definition().Operation(op)
	who := "an unidentified caller"
	if p, ok := PrincipalFrom(ctx); ok {
		who = fmt.Sprintf("%s via %s", p.Kind, p.Via)
		if p.Client != "" {
			who += " (" + p.Client + ")"
		}
	}
	call, _ := beginAudit(ctx, sink, s.logger, auditSpec{
		connector: "local", operation: op, op: contractOp, known: true, resources: sameResource(res, s.ResourceDef),
		config: map[string]any{localconn.InputID: id}, acknowledged: ApplyMutationOptions(options).Acknowledged,
		automation: true, automationVia: "standby",
		reason: fmt.Sprintf("standby: %s of the flagged resource, requested by %s on the %s surface", op, who, CallerSurfaceFrom(ctx)),
	})
	dr := resourceDefToDomain(res)
	var message string
	switch op {
	case localconn.OpApply:
		var applyRes localconn.ApplyResult
		applyRes, err = s.localConnector().Apply(ctx, dr)
		if err == nil {
			message = localconn.FormatApplyResultMessage(id, spec, applyRes) + " (standby)"
		}
	case localconn.OpStop:
		err = s.localConnector().Stop(ctx, dr)
		message = fmt.Sprintf("resource %q stopped (standby); install state preserved", id)
	case localconn.OpReload:
		err = s.localConnector().Reload(ctx, dr)
		message = fmt.Sprintf("resource %q reloaded (standby)", id)
	default:
		err = fmt.Errorf("operation %q is not allowed in standby", op)
	}
	if err != nil {
		call.finish(externalConnectorError(ExternalConnectorOperationArgs{Connector: "local", Operation: op}, ExternalConnectorOperationFailed, err))
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	call.finish(nil)
	return &OpResult{Success: true, ServiceID: id, Message: message}, nil
}

// StopDevSessions stops every running dev session without writing a pause
// flag, and returns their ids. Standby calls it on entry.
func (s *ResourceRuntimeService) StopDevSessions(ctx context.Context) ([]string, error) {
	cfg := s.snapshotConfig()
	if cfg == nil {
		return nil, nil
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	var stopped []string
	var errs []string
	for i := range cfg.Resources {
		res := cfg.Resources[i]
		if !isLocalDevSession(res) {
			continue
		}
		dr := resourceDefToDomain(&res)
		state, err := s.localConnector().Status(ctx, dr)
		if err != nil || resourceStateDown(state) {
			continue
		}
		if err := s.localConnector().Stop(ctx, dr); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", res.ID, err))
			continue
		}
		stopped = append(stopped, res.ID)
	}
	if len(errs) > 0 {
		return stopped, fmt.Errorf("dev sessions that did not stop: %s", strings.Join(errs, "; "))
	}
	return stopped, nil
}

// StartDevSessions starts the listed dev sessions again, recording each as
// Cerberus acting on its own in the main chain. Standby calls it on exit.
// A session already running is left as it is; an id that is no longer a
// local dev session is named in the error.
func (s *ResourceRuntimeService) StartDevSessions(ctx context.Context, ids []string) error {
	var errs []string
	for _, id := range ids {
		res, ok := s.ResourceDef(id)
		if !ok || !isLocalDevSession(*res) {
			errs = append(errs, id+": no longer a local dev session")
			continue
		}
		dr := resourceDefToDomain(res)
		s.opMu.Lock()
		state, statusErr := s.localConnector().Status(ctx, dr)
		s.opMu.Unlock()
		if statusErr == nil && !resourceStateDown(state) {
			continue // already running: started by hand, or by the new build
		}
		applyOp, _ := localconn.Definition().Operation(localconn.OpApply)
		call, _ := beginAudit(ctx, s.audit, s.logger, auditSpec{
			connector: "local", operation: localconn.OpApply, op: applyOp, known: true, resources: sameResource(res, s.ResourceDef),
			config: map[string]any{localconn.InputID: id}, automation: true, automationVia: "standby",
			reason: "standby ended: starting a dev session standby stopped",
		})
		s.opMu.Lock()
		_, err := s.localConnector().Apply(ctx, dr)
		s.opMu.Unlock()
		if err != nil {
			call.finish(externalConnectorError(ExternalConnectorOperationArgs{Connector: "local", Operation: localconn.OpApply}, ExternalConnectorOperationFailed, err))
			errs = append(errs, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		call.finish(nil)
	}
	if len(errs) > 0 {
		return fmt.Errorf("dev sessions not started: %s", strings.Join(errs, "; "))
	}
	return nil
}

func isLocalDevSession(res config.ResourceDef) bool {
	if res.Connector != "local" {
		return false
	}
	spec, err := localconn.SpecFromResourceConfig(res.Config)
	if err != nil {
		return false
	}
	return res.Type == string(domain.ResourceProcess) && (spec.Mode == "" || spec.Mode == localconn.ProcessModeDevSession)
}

// standbyMarker marks a read answered in standby: the header always, and
// "standby": true in a JSON object body. A body that is not a JSON object
// (a list, a stream) keeps only the header.
type standbyMarker struct {
	http.ResponseWriter
	resource  string
	buf       bytes.Buffer
	status    int
	buffering bool
	passed    bool
}

func newStandbyMarker(w http.ResponseWriter, resource string) *standbyMarker {
	w.Header().Set(StandbyHeader, resource)
	return &standbyMarker{ResponseWriter: w, resource: resource, status: http.StatusOK}
}

func (m *standbyMarker) WriteHeader(status int) {
	if m.buffering || m.passed {
		return
	}
	m.status = status
	if strings.HasPrefix(m.Header().Get("Content-Type"), "application/json") {
		m.buffering = true
		return
	}
	m.passed = true
	m.ResponseWriter.WriteHeader(status)
}

func (m *standbyMarker) Write(p []byte) (int, error) {
	if !m.buffering && !m.passed {
		m.WriteHeader(http.StatusOK)
	}
	if m.buffering {
		return m.buf.Write(p)
	}
	return m.ResponseWriter.Write(p)
}

// Flush turns a buffered response into a pass-through stream.
func (m *standbyMarker) Flush() {
	if m.buffering {
		m.buffering, m.passed = false, true
		m.ResponseWriter.WriteHeader(m.status)
		_, _ = m.ResponseWriter.Write(m.buf.Bytes())
		m.buf.Reset()
	}
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// finish writes a buffered body, marked when it is a JSON object.
func (m *standbyMarker) finish() {
	if !m.buffering {
		return
	}
	body := m.buf.Bytes()
	var obj map[string]json.RawMessage
	// A body that already says standby (the daemon status) keeps its own.
	if json.Unmarshal(body, &obj) == nil && obj != nil && obj["standby"] == nil {
		obj["standby"] = json.RawMessage("true")
		if marked, err := json.MarshalIndent(obj, "", "  "); err == nil {
			body = append(marked, '\n')
		}
	}
	m.Header().Del("Content-Length")
	m.ResponseWriter.WriteHeader(m.status)
	_, _ = m.ResponseWriter.Write(body)
}
