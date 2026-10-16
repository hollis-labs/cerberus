package eventexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
)

// Config is what an Exporter needs.
type Config struct {
	// AuditDir and ApprovalsDir are the stores it reads.
	AuditDir, ApprovalsDir string
	// OutboxDir holds the outbox of the audit channel (OutboxDir/audit).
	OutboxDir string
	// Grace is how long an approval-store event may wait for its audit record
	// before cerberus.audit_gap is raised. Default 60s.
	Grace time.Duration
	// Batch bounds the records read from one source per Step. Default 200.
	Batch int
	// ResumeCheck is how often a stopped source looks for a reanchor. Default 30s.
	ResumeCheck time.Duration
	Now         func() time.Time
}

// Source states.
const (
	StateRunning = "running"
	StateStopped = "stopped"
)

// SourceStatus is the visible state of one source. A stopped source is not an
// error to ignore: it means export of that source is halted at a problem in the
// authoritative chain, and says which.
type SourceStatus struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Exported is the number of entries appended since the exporter started.
	Exported uint64 `json:"exported"`
}

// Status is the exporter's visible state.
type Status struct {
	Audit      SourceStatus `json:"audit"`
	Approval   SourceStatus `json:"approval"`
	Generation uint64       `json:"generation"`
	Head       uint64       `json:"head"`
	LastStep   time.Time    `json:"last_step,omitempty"`
	LastError  string       `json:"last_error,omitempty"`
}

// Exporter reads the audit chain and the approval store with the strict
// readers, maps what it reads and appends it to the audit-channel outbox. It
// never writes to either store.
type Exporter struct {
	cfg    Config
	outbox *FileOutbox
	gaps   *GapDetector

	mu         sync.Mutex
	check      checkpoint
	status     Status
	lastResume map[string]time.Time
}

// checkpoint is where each source's reader stands. The outbox is the ledger; the
// checkpoint only lets a restart skip records that map to no event. A lost or
// stale checkpoint costs a re-read, never a duplicate, because every entry has
// a deduplication key.
type checkpoint struct {
	Audit    audit.Cursor    `json:"audit"`
	Approval approval.Cursor `json:"approval"`
}

const checkpointFile = "checkpoint.json"

// New opens the audit-channel outbox and prepares to export. It refuses
// (ErrRefused) when the outbox is inconsistent, and when a source no longer
// holds the place the checkpoint records, as after a restore of either side:
// the exporter never guesses its way past a mismatch.
//
// With no checkpoint and an empty outbox this is the first enable: each source
// starts at its verified tail, with no back-fill.
func New(cfg Config) (*Exporter, error) {
	if cfg.Grace <= 0 {
		cfg.Grace = time.Minute
	}
	if cfg.Batch <= 0 {
		cfg.Batch = 200
	}
	if cfg.ResumeCheck <= 0 {
		cfg.ResumeCheck = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	box, err := OpenFileOutbox(filepath.Join(cfg.OutboxDir, ChannelAudit))
	if err != nil {
		return nil, err
	}
	e := &Exporter{cfg: cfg, outbox: box, gaps: NewGapDetector(cfg.Grace), lastResume: map[string]time.Time{}}
	e.status.Generation = box.Generation()
	e.status.Audit.State, e.status.Approval.State = StateRunning, StateRunning
	if err := e.loadCheckpoint(); err != nil {
		return nil, err
	}
	for name, check := range map[string]func() (bool, error){
		"audit":    func() (bool, error) { return audit.CursorValid(cfg.AuditDir, e.check.Audit) },
		"approval": func() (bool, error) { return approval.CursorValid(cfg.ApprovalsDir, e.check.Approval) },
	} {
		ok, err := check()
		if err != nil {
			return nil, fmt.Errorf("eventexport: check the %s checkpoint: %w", name, err)
		}
		if !ok {
			return nil, fmt.Errorf("%w: the %s store does not hold the place the checkpoint records (restored or rewritten?); move %s aside to start a new outbox with a new generation", ErrRefused, name, e.outboxDir())
		}
	}
	e.seedGaps()
	return e, nil
}

func (e *Exporter) outboxDir() string { return filepath.Join(e.cfg.OutboxDir, ChannelAudit) }

func (e *Exporter) loadCheckpoint() error {
	path := filepath.Join(e.outboxDir(), checkpointFile)
	data, err := os.ReadFile(path) //nolint:gosec // the outbox's own file
	switch {
	case err == nil:
		if jsonErr := json.Unmarshal(data, &e.check); jsonErr != nil {
			return fmt.Errorf("%w: the checkpoint is unreadable: %w", ErrRefused, jsonErr)
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("eventexport: read checkpoint: %w", err)
	}
	if _, has, _ := e.outbox.Head(); has {
		return fmt.Errorf("%w: the outbox has entries but no checkpoint", ErrRefused)
	}
	// First enable: start at the verified tail of each source.
	auditTail, err := audit.TailCursor(e.cfg.AuditDir)
	if err != nil {
		return fmt.Errorf("eventexport: cannot start from the audit chain's tail: %w", err)
	}
	approvalTail, err := approval.TailCursor(e.cfg.ApprovalsDir)
	if err != nil {
		return fmt.Errorf("eventexport: cannot start from the approval store's tail: %w", err)
	}
	e.check = checkpoint{Audit: auditTail, Approval: approvalTail}
	return e.saveCheckpoint()
}

func (e *Exporter) saveCheckpoint() error {
	data, err := json.Marshal(e.check)
	if err != nil {
		return err
	}
	dir := e.outboxDir()
	tmp := filepath.Join(dir, checkpointFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the outbox's own file
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, checkpointFile))
}

// seedGaps loads the recent approval audit records, so a restart does not make a
// store event look unmatched.
func (e *Exporter) seedGaps() {
	recs, err := audit.ReadRecords(e.cfg.AuditDir)
	if err != nil {
		return
	}
	cutoff := e.cfg.Now().Add(-2 * e.cfg.Grace)
	for _, rec := range recs {
		if rec.Time.After(cutoff) {
			e.gaps.ObserveAudit(rec)
		}
	}
}

// Status returns the exporter's visible state.
func (e *Exporter) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.status
	if head, ok, _ := e.outbox.Head(); ok {
		s.Head = head.Seq
	}
	return s
}

// Outbox is the audit-channel outbox, for a relay to read.
func (e *Exporter) Outbox() Outbox { return e.outbox }

// Run steps every interval until ctx ends. A failed step is recorded in Status
// and retried on the next tick; onStatus (optional) sees the status after each.
func (e *Exporter) Run(ctx context.Context, interval time.Duration, onError func(error), onStatus func(Status)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := e.Step(ctx); err != nil && onError != nil {
			onError(err)
		}
		if onStatus != nil {
			onStatus(e.Status())
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Step does one bounded pass: the audit chain, then the approval store, then
// any gap alerts that have come due.
func (e *Exporter) Step(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.cfg.Now()
	e.status.LastStep = now
	err := errors.Join(e.stepAudit(ctx, now), e.stepApproval(ctx, now), e.stepGaps(now))
	e.status.LastError = ""
	if err != nil {
		e.status.LastError = err.Error()
	}
	e.writeStatus()
	return err
}

// StatusFile is the exporter's status as JSON, rewritten after every step, so a
// stopped source is visible to anyone who looks (not only to whoever sees the
// alert) without asking the daemon.
const StatusFile = "status.json"

func (e *Exporter) writeStatus() {
	s := e.status
	if head, ok, _ := e.outbox.Head(); ok {
		s.Head = head.Seq
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(e.outboxDir(), StatusFile+".tmp")
	if os.WriteFile(tmp, append(data, '\n'), 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(e.outboxDir(), StatusFile))
	}
}

func (e *Exporter) append(key string, src SourceRef, env Envelope, counter *uint64) error {
	_, created, err := e.outbox.Append(key, src, env)
	if err == nil && created {
		*counter++
	}
	return err
}

func (e *Exporter) stepAudit(ctx context.Context, now time.Time) error {
	st := &e.status.Audit
	if st.State == StateStopped && !e.tryResumeAudit(now) {
		return nil
	}
	batch, err := audit.ReadFrom(e.cfg.AuditDir, e.check.Audit, e.cfg.Batch)
	if err != nil {
		return err
	}
	for _, rec := range batch.Records {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.gaps.ObserveAudit(rec)
		for _, m := range MapAudit(rec) {
			if err := e.append(m.Key, SourceRef{Source: "audit", Seq: rec.Seq}, m.Envelope, &st.Exported); err != nil {
				return err
			}
		}
	}
	if batch.Next != e.check.Audit {
		e.check.Audit = batch.Next
		if err := e.saveCheckpoint(); err != nil {
			return err
		}
	}
	if batch.Stop == nil {
		*st = SourceStatus{State: StateRunning, Exported: st.Exported}
		return nil
	}
	*st = SourceStatus{State: StateStopped, Reason: string(batch.Stop.Reason), Detail: batch.Stop.Detail, Exported: st.Exported}
	env := alert("alert.raised", RuleAuditChainBroken, "audit export stopped at a problem in the audit chain: "+string(batch.Stop.Reason),
		"critical", now, "msg://audit/cerberus/chain", []string{"audit", "export_stopped", string(batch.Stop.Reason)},
		map[string]any{"after_audit_seq": batch.Next.Seq}, "audit-chain-broken", ChannelAudit)
	return e.append(fmt.Sprintf("stop/audit/%s/%d", batch.Stop.Reason, batch.Next.Seq), SourceRef{Source: "audit"}, env, &st.Exported)
}

// tryResumeAudit looks, at most every ResumeCheck, for a reanchor that lets a
// stopped audit export carry on, and raises alert.cleared when it does.
func (e *Exporter) tryResumeAudit(now time.Time) bool {
	if last, ok := e.lastResume["audit"]; ok && now.Sub(last) < e.cfg.ResumeCheck {
		return false
	}
	e.lastResume["audit"] = now
	next, ok, err := audit.ResumeAfterReanchor(e.cfg.AuditDir, e.check.Audit)
	if err != nil || !ok {
		return false
	}
	st := &e.status.Audit
	env := alert("alert.cleared", RuleAuditChainBroken, "the audit chain was reanchored; audit export resumed", "info", now,
		"msg://audit/cerberus/chain", []string{"audit", "export_resumed"}, map[string]any{"audit_seq": next.Seq}, "audit-chain-broken", ChannelAudit)
	if err := e.append(fmt.Sprintf("a/%d", next.Seq), SourceRef{Source: "audit", Seq: next.Seq}, env, &st.Exported); err != nil {
		return false
	}
	e.check.Audit = next
	if err := e.saveCheckpoint(); err != nil {
		return false
	}
	*st = SourceStatus{State: StateRunning, Exported: st.Exported}
	return true
}

func (e *Exporter) stepApproval(ctx context.Context, now time.Time) error {
	st := &e.status.Approval
	if st.State == StateStopped && !e.tryResumeApproval(now) {
		return nil
	}
	batch, err := approval.ReadFrom(e.cfg.ApprovalsDir, e.check.Approval, e.cfg.Batch)
	if err != nil {
		return err
	}
	for _, off := range batch.Damaged {
		env := alert("alert.raised", RuleApprovalChainBreak, "a line of the approval store did not parse and was passed over", "warning", now,
			"msg://approval/cerberus/store", []string{"approval", "damaged_line"}, map[string]any{"offset": off}, fmt.Sprintf("approval-damaged-line/%d", off), ChannelAudit)
		if err := e.append(fmt.Sprintf("damaged/approval/%d", off), SourceRef{Source: "approval"}, env, &st.Exported); err != nil {
			return err
		}
	}
	for _, ev := range batch.Events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.gaps.ObserveApproval(ev)
		if m := MapApproval(ev); m != nil {
			if err := e.append(m.Key, SourceRef{Source: "approval", Seq: ev.Seq}, m.Envelope, &st.Exported); err != nil {
				return err
			}
		}
	}
	if batch.Next != e.check.Approval {
		e.check.Approval = batch.Next
		if err := e.saveCheckpoint(); err != nil {
			return err
		}
	}
	if batch.Stop == nil {
		*st = SourceStatus{State: StateRunning, Exported: st.Exported}
		return nil
	}
	*st = SourceStatus{State: StateStopped, Reason: string(batch.Stop.Reason), Detail: batch.Stop.Detail, Exported: st.Exported}
	env := alert("alert.raised", RuleApprovalChainBreak, "approval export stopped at a break in the approval store's own chain: "+string(batch.Stop.Reason),
		"critical", now, "msg://approval/cerberus/store", []string{"approval", "export_stopped", string(batch.Stop.Reason)},
		map[string]any{"after_approval_seq": batch.Next.Seq}, "approval-chain-break", ChannelAudit)
	return e.append(fmt.Sprintf("stop/approval/%s/%d", batch.Stop.Reason, batch.Next.Seq), SourceRef{Source: "approval"}, env, &st.Exported)
}

func (e *Exporter) tryResumeApproval(now time.Time) bool {
	if last, ok := e.lastResume["approval"]; ok && now.Sub(last) < e.cfg.ResumeCheck {
		return false
	}
	e.lastResume["approval"] = now
	next, ok, err := approval.ResumeAfterReanchor(e.cfg.ApprovalsDir, e.check.Approval)
	if err != nil || !ok {
		return false
	}
	st := &e.status.Approval
	env := alert("alert.cleared", RuleApprovalChainBreak, "the approval store was taken up again; approval export resumed", "info", now,
		"msg://approval/cerberus/store", []string{"approval", "export_resumed"}, map[string]any{"approval_seq": next.Seq}, "approval-chain-break", ChannelAudit)
	if err := e.append(fmt.Sprintf("resume/approval/%d", next.Seq), SourceRef{Source: "approval", Seq: next.Seq}, env, &st.Exported); err != nil {
		return false
	}
	e.check.Approval = next
	if err := e.saveCheckpoint(); err != nil {
		return false
	}
	*st = SourceStatus{State: StateRunning, Exported: st.Exported}
	return true
}

// stepGaps raises cerberus.audit_gap for approval-store events whose audit record
// never arrived. It does not create the missing record and invents no digest.
func (e *Exporter) stepGaps(now time.Time) error {
	if e.status.Audit.State == StateStopped {
		return nil // the audit side is not being read, so a missing record proves nothing
	}
	var errs []error
	for _, gap := range e.gaps.Due(now) {
		env := alert("alert.raised", RuleAuditGap, "an approval event has no matching audit record", "warning", now,
			"msg://approval/cerberus/"+gap.ApprovalID, []string{"audit", "gap", gap.Transition},
			map[string]any{"approval_id": gap.ApprovalID, "transition": gap.Transition, "store_time": gap.StoreTime.UTC().Format(time.RFC3339)}, gap.Key(), ChannelAudit)
		env.Corr = corr(gap.ApprovalID)
		if err := e.append(gap.Key(), SourceRef{Source: "gap"}, env, &e.status.Approval.Exported); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
