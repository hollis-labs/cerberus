package eventexport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/scheduling"
)

type rig struct {
	t                      *testing.T
	root                   string
	auditDir, approvalsDir string
	sink                   *audit.FileSink
	store                  *approval.Store
	now                    time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{t: t, root: root, auditDir: filepath.Join(root, "audit"), approvalsDir: filepath.Join(root, "approvals"), now: time.Now().UTC()}
	var err error
	if r.sink, err = audit.OpenFileSink(r.auditDir); err != nil {
		t.Fatal(err)
	}
	if r.store, err = approval.Open(r.approvalsDir); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) exporter(mutate ...func(*Config)) (*Exporter, error) {
	cfg := Config{AuditDir: r.auditDir, ApprovalsDir: r.approvalsDir, OutboxDir: filepath.Join(r.root, "events"), Grace: time.Minute, Now: func() time.Time { return r.now }}
	for _, m := range mutate {
		m(&cfg)
	}
	return New(cfg)
}

func (r *rig) must(e *Exporter, err error) *Exporter {
	r.t.Helper()
	if err != nil {
		r.t.Fatal(err)
	}
	return e
}

func (r *rig) intent(op string) {
	r.t.Helper()
	if _, err := r.sink.Write(audit.Record{Kind: audit.KindIntent, OperationID: op, Operation: "delete_pod"}); err != nil {
		r.t.Fatal(err)
	}
}

// request asks for an approval in the store and, like the broker, writes the audit record after it.
func (r *rig) request(withAudit bool) approval.Approval {
	r.t.Helper()
	a, err := r.store.Request(approval.Approval{Principal: audit.Principal{Kind: "agent", Subject: "agent:a", AuthMethod: "bearer"}, Connector: "kubernetes",
		Operation: "delete_pod", Effect: "destructive", Rule: "builtin.env-prod", Channel: approval.ChannelTTYConfirm}, time.Hour)
	if err != nil {
		r.t.Fatal(err)
	}
	if withAudit {
		r.auditApproval(audit.KindApprovalRequested, a.ID)
	}
	return a
}

func (r *rig) auditApproval(kind, id string) {
	r.t.Helper()
	if _, err := r.sink.Write(audit.Record{Kind: kind, ApprovalID: id}); err != nil {
		r.t.Fatal(err)
	}
}

func step(t *testing.T, e *Exporter) {
	t.Helper()
	if err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func kinds(t *testing.T, e *Exporter) []string {
	t.Helper()
	entries, err := e.Outbox().After(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(entries))
	for i, en := range entries {
		out[i] = en.Envelope.Kind
	}
	return out
}

func digestOf(t *testing.T, dirs ...string) [32]byte {
	t.Helper()
	h := sha256.New()
	for _, dir := range dirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && !strings.HasSuffix(path, ".lock") {
				data, _ := os.ReadFile(path) //nolint:gosec // the test's own file
				h.Write([]byte(path))
				h.Write(data)
			}
			return nil
		})
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func TestFirstEnableStartsAtTheTailThenExportsInOrderWithoutDuplicatesAcrossRestarts(t *testing.T) {
	r := newRig(t)
	r.intent("old-1") // history before the exporter existed
	r.request(true)

	e := r.must(r.exporter())
	step(t, e)
	if got := kinds(t, e); len(got) != 0 {
		t.Fatalf("history was back-filled: %v", got)
	}

	r.intent("op-1")
	a := r.request(true)
	if _, err := r.store.Decide(a.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Subject: "user:chris", AuthMethod: "passkey"}}, nil); err != nil {
		t.Fatal(err)
	}
	r.auditApproval(audit.KindApprovalDecided, a.ID)
	step(t, e)
	// audit chain first (intent), then the approval store (requested, decided);
	// the approval_requested/decided audit records add nothing.
	want := []string{"audit.recorded", "approval.requested", "approval.resolved"}
	if got := kinds(t, e); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	entries, _ := e.Outbox().After(0, 0)
	for i, en := range entries {
		if en.Seq != uint64(i+1) || en.Envelope.Sequence != en.Seq || en.Envelope.Generation != e.Status().Generation || en.Envelope.Source.Channel != "audit" {
			t.Fatalf("entry %d = %+v", i, en)
		}
	}
	ids := map[string]bool{}
	for _, en := range entries {
		ids[en.Envelope.ID] = true
	}

	// A restart resumes where it left off: nothing repeats, ids are unchanged.
	step(t, e)
	again := r.must(r.exporter())
	step(t, again)
	step(t, again)
	entries2, _ := again.Outbox().After(0, 0)
	if len(entries2) != len(entries) {
		t.Fatalf("a restart re-exported: %d -> %d", len(entries), len(entries2))
	}
	for _, en := range entries2 {
		if !ids[en.Envelope.ID] {
			t.Fatalf("an id changed across restart: %s", en.Envelope.ID)
		}
	}
	r.intent("op-2")
	step(t, again)
	if got := kinds(t, again); len(got) != len(want)+1 {
		t.Fatalf("new work after restart: %v", got)
	}
}

func TestExportNeverWritesTheAuthoritativeStores(t *testing.T) {
	r := newRig(t)
	e := r.must(r.exporter())
	r.intent("op-1")
	a := r.request(true)
	r.auditApproval(audit.KindApprovalConsumed, a.ID)
	before := digestOf(t, r.auditDir, r.approvalsDir)
	step(t, e)
	step(t, e)
	if digestOf(t, r.auditDir, r.approvalsDir) != before {
		t.Fatal("export changed an authoritative store")
	}
	if err := audit.Verify(r.auditDir); err != nil {
		t.Fatalf("the chain no longer verifies: %v", err)
	}
	if len(kinds(t, e)) == 0 {
		t.Fatal("nothing was exported")
	}
}

func TestRecordsThatMapToNothingStillAdvanceTheCheckpoint(t *testing.T) {
	r := newRig(t)
	e := r.must(r.exporter(func(c *Config) { c.Batch = 2 }))
	a := r.request(false)
	for range 5 { // only approval audit kinds: none is exported
		r.auditApproval(audit.KindApprovalRequested, a.ID)
	}
	r.intent("op-last")
	for range 6 {
		step(t, e)
	}
	got := kinds(t, e)
	if len(got) < 2 || got[len(got)-1] != "audit.recorded" {
		t.Fatalf("the reader stalled behind skipped records: %v", got)
	}
}

func TestAnApprovalEventWithNoAuditRecordRaisesOneGapAlertAfterTheGrace(t *testing.T) {
	r := newRig(t)
	e := r.must(r.exporter())
	matched := r.request(true)
	missing := r.request(false)
	step(t, e)
	r.now = r.now.Add(30 * time.Second)
	step(t, e)
	for _, en := range mustAfter(t, e) {
		if en.Envelope.Kind == "alert.raised" {
			t.Fatalf("an alert before the grace window: %+v", en.Envelope)
		}
	}
	r.now = r.now.Add(45 * time.Second)
	step(t, e)
	step(t, e)
	var gaps []Entry
	for _, en := range mustAfter(t, e) {
		if en.Envelope.Kind == "alert.raised" {
			gaps = append(gaps, en)
		}
	}
	if len(gaps) != 1 || !strings.Contains(string(gaps[0].Envelope.Payload), RuleAuditGap) || !strings.Contains(string(gaps[0].Envelope.Payload), missing.ID) || strings.Contains(string(gaps[0].Envelope.Payload), matched.ID) {
		t.Fatalf("gap alerts = %+v", gaps)
	}
	// Not repeated after a restart, and nothing was invented in the audit chain.
	again := r.must(r.exporter())
	r.now = r.now.Add(10 * time.Minute)
	step(t, again)
	count := 0
	for _, en := range mustAfter(t, again) {
		if en.Envelope.Kind == "alert.raised" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the gap alert was repeated: %d", count)
	}
	recs, _ := audit.ReadRecords(r.auditDir)
	for _, rec := range recs {
		if rec.ApprovalID == missing.ID {
			t.Fatal("a missing audit record was synthesized")
		}
	}
}

func mustAfter(t *testing.T, e *Exporter) []Entry {
	t.Helper()
	entries, err := e.Outbox().After(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestAuditChainDamageStopsAuditExportVisiblyAndResumesAfterAReanchor(t *testing.T) {
	r := newRig(t)
	e := r.must(r.exporter())
	r.intent("op-1")
	r.intent("op-2")
	step(t, e)
	exported := len(mustAfter(t, e))

	files, _ := filepath.Glob(filepath.Join(r.auditDir, "*.jsonl"))
	data, _ := os.ReadFile(files[0]) //nolint:gosec // the test's own file
	r.intent("op-3")
	r.intent("op-4")
	data2, _ := os.ReadFile(files[0]) //nolint:gosec // the test's own file
	// Tamper with op-3 (a record not yet exported).
	tampered := bytes.Replace(data2, []byte(`"operation_id":"op-3"`), []byte(`"operation_id":"OP-3"`), 1)
	if bytes.Equal(tampered, data2) || len(data2) <= len(data) {
		t.Fatal("test setup failed")
	}
	if err := os.WriteFile(files[0], tampered, 0o600); err != nil { //nolint:gosec // the test's own file
		t.Fatal(err)
	}
	step(t, e)
	st := e.Status()
	if st.Audit.State != StateStopped || st.Audit.Reason != string(audit.StopBroken) || st.Approval.State != StateRunning {
		t.Fatalf("status = %+v", st)
	}
	// The stop is visible on disk too, not only in the alert.
	onDisk, err := os.ReadFile(filepath.Join(r.root, "events", ChannelAudit, StatusFile)) //nolint:gosec // the test's own file
	if err != nil || !strings.Contains(string(onDisk), `"state": "stopped"`) || !strings.Contains(string(onDisk), "broken_chain") {
		t.Fatalf("status file = %s (%v)", onDisk, err)
	}
	entries := mustAfter(t, e)
	if len(entries) != exported+1 || entries[len(entries)-1].Envelope.Kind != "alert.raised" || !strings.Contains(string(entries[len(entries)-1].Envelope.Payload), RuleAuditChainBroken) {
		t.Fatalf("expected exactly one stop alert, got %d new entries: %+v", len(entries)-exported, entries[exported:])
	}
	// It stays stopped, quietly: no repeat alert and no further audit entries.
	step(t, e)
	step(t, e)
	if got := len(mustAfter(t, e)); got != exported+1 {
		t.Fatalf("entries grew while stopped: %d", got)
	}

	// A person reanchors; after the check interval export resumes and says so.
	if _, err := audit.Reanchor(r.sink, r.auditDir, audit.Principal{Surface: "test"}); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Minute)
	step(t, e)
	if st := e.Status(); st.Audit.State != StateRunning {
		t.Fatalf("status after reanchor = %+v", st)
	}
	r.intent("op-5")
	step(t, e)
	last := mustAfter(t, e)
	if k := last[len(last)-1].Envelope.Kind; k != "audit.recorded" {
		t.Fatalf("the newest entry is %s", k)
	}
	var cleared int
	for _, en := range last {
		if en.Envelope.Kind == "alert.cleared" {
			cleared++
		}
	}
	if cleared != 1 {
		t.Fatalf("alert.cleared entries = %d", cleared)
	}
}

func TestApprovalStoreChainBreakAlertsAndStopsOnlyThatSource(t *testing.T) {
	r := newRig(t)
	e := r.must(r.exporter())
	r.request(true)
	step(t, e)
	base := len(mustAfter(t, e))

	f, _ := os.OpenFile(filepath.Join(r.approvalsDir, approval.FileName), os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the test's own file
	_, _ = f.WriteString(`{"v":1,"seq":99,"time":"2026-10-16T12:00:00Z","type":"expired","approval_id":"apr_x","prev_hash":"bad","hash":"bad"}` + "\n")
	_ = f.Close()
	r.intent("still-flows")
	step(t, e)
	st := e.Status()
	if st.Approval.State != StateStopped || st.Audit.State != StateRunning {
		t.Fatalf("status = %+v", st)
	}
	entries := mustAfter(t, e)
	var rules, audits int
	for _, en := range entries[base:] {
		if strings.Contains(string(en.Envelope.Payload), RuleApprovalChainBreak) {
			rules++
		}
		if en.Envelope.Kind == "audit.recorded" {
			audits++
		}
	}
	if rules != 1 || audits != 1 {
		t.Fatalf("approval_chain_break alerts %d, audit records %d", rules, audits)
	}
	// The daemon taking the store up again (Open appends reanchored) resumes it.
	if _, err := approval.Open(r.approvalsDir); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Minute)
	step(t, e)
	if st := e.Status(); st.Approval.State != StateRunning {
		t.Fatalf("status after reanchor = %+v", st)
	}
}

func TestStartRefusesWhenASourceDoesNotHoldTheCheckpoint(t *testing.T) {
	r := newRig(t)
	for range 5 {
		r.intent("op")
	}
	e := r.must(r.exporter())
	r.intent("more")
	step(t, e)

	// The audit directory is replaced by an older copy: fewer records than the checkpoint.
	files, _ := filepath.Glob(filepath.Join(r.auditDir, "*.jsonl"))
	data, _ := os.ReadFile(files[0]) //nolint:gosec // the test's own file
	lines := bytes.SplitAfter(data, []byte("\n"))
	if err := os.WriteFile(files[0], bytes.Join(lines[:3], nil), 0o600); err != nil { //nolint:gosec // the test's own file
		t.Fatal(err)
	}
	if _, err := r.exporter(); !errors.Is(err, ErrRefused) {
		t.Fatalf("a restored source was accepted: %v", err)
	}
}

func TestStartRefusesAnOutboxWithEntriesAndNoCheckpoint(t *testing.T) {
	r := newRig(t)
	e := r.must(r.exporter())
	r.intent("op-1")
	step(t, e)
	if err := os.Remove(filepath.Join(r.root, "events", ChannelAudit, checkpointFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.exporter(); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v", err)
	}
}

func TestATornOutboxTailIsRepairedAndExportCarriesOn(t *testing.T) {
	r := newRig(t)
	e := r.must(r.exporter())
	r.intent("op-1")
	r.intent("op-2")
	step(t, e)
	n := len(mustAfter(t, e))
	path := filepath.Join(r.root, "events", ChannelAudit, outboxFile)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the test's own file
	_, _ = f.WriteString(`{"seq":99,"prev":"x","key":"a/99","env":{"kind":"audit.rec`)
	_ = f.Close()

	again := r.must(r.exporter())
	r.intent("op-3")
	step(t, again)
	if got := len(mustAfter(t, again)); got != n+1 {
		t.Fatalf("entries after repair = %d, want %d", got, n+1)
	}
}

func TestRunStopsWithItsContext(t *testing.T) {
	r := newRig(t)
	e := r.must(r.exporter())
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { e.Run(ctx, 10*time.Millisecond, func(error) {}, nil); close(done) }()
	r.intent("op-1")
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
	if len(mustAfter(t, e)) == 0 {
		t.Fatal("Run exported nothing")
	}
}

// The sink's own heal of a torn write is a chain_break the chain verifies through:
// export carries on and reports the raised/cleared pair.
func TestATornAuditWriteIsReadThroughAndReportedAsAPair(t *testing.T) {
	r := newRig(t)
	e := r.must(r.exporter())
	r.intent("op-1")
	step(t, e)
	files, _ := filepath.Glob(filepath.Join(r.auditDir, "*.jsonl"))
	f, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the test's own file
	_, _ = f.WriteString(`{"v":1,"seq":9,"id":"torn`)             // a crash mid-write
	_ = f.Close()
	r.intent("op-2") // the sink heals the tear with a chain_break, then writes op-2
	step(t, e)
	if st := e.Status(); st.Audit.State != StateRunning {
		t.Fatalf("status = %+v", st)
	}
	var raised, cleared, audits int
	for _, en := range mustAfter(t, e) {
		switch en.Envelope.Kind {
		case "alert.raised":
			if strings.Contains(string(en.Envelope.Payload), RuleAuditTornWrite) {
				raised++
			}
		case "alert.cleared":
			if strings.Contains(string(en.Envelope.Payload), RuleAuditTornWrite) {
				cleared++
			}
		case "audit.recorded":
			audits++
		}
	}
	if raised != 1 || cleared != 1 || audits != 2 {
		t.Fatalf("raised %d cleared %d audit.recorded %d", raised, cleared, audits)
	}
	if err := audit.Verify(r.auditDir); err != nil {
		t.Fatalf("test assumption: the chain verifies through the heal: %v", err)
	}
}

func payloadMap(t *testing.T, en Entry) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(en.Envelope.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// coalescePair returns the coalesce keys of the raised and the cleared alerts of
// rule, failing unless there is at least one of each.
func coalescePair(t *testing.T, entries []Entry, rule string) (raised, cleared []string) {
	t.Helper()
	for _, en := range entries {
		p := payloadMap(t, en)
		if p["rule_id"] != rule {
			continue
		}
		key, _ := p["coalesce_key"].(string)
		switch en.Envelope.Kind {
		case "alert.raised":
			raised = append(raised, key)
		case "alert.cleared":
			cleared = append(cleared, key)
		}
	}
	if len(raised) == 0 || len(cleared) == 0 {
		t.Fatalf("rule %s: raised %v cleared %v", rule, raised, cleared)
	}
	return raised, cleared
}

// A cleared alert must coalesce with the raised alert it clears, or a dashboard
// keeps the problem open.
func TestRaisedAndClearedAlertsShareACoalesceKey(t *testing.T) {
	// audit chain: tamper, stop, reanchor, resume
	r := newRig(t)
	e := r.must(r.exporter())
	r.intent("op-1")
	step(t, e)
	files, _ := filepath.Glob(filepath.Join(r.auditDir, "*.jsonl"))
	r.intent("op-2")
	data, _ := os.ReadFile(files[0])                                                                                                                //nolint:gosec // the test's own file
	if err := os.WriteFile(files[0], bytes.Replace(data, []byte(`"operation_id":"op-2"`), []byte(`"operation_id":"OP-2"`), 1), 0o600); err != nil { //nolint:gosec // the test's own file
		t.Fatal(err)
	}
	step(t, e)
	if _, err := audit.Reanchor(r.sink, r.auditDir, audit.Principal{Surface: "test"}); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Minute)
	step(t, e)
	raised, cleared := coalescePair(t, mustAfter(t, e), RuleAuditChainBroken)
	if raised[0] == "" || raised[0] != cleared[0] || len(raised) != 1 || len(cleared) != 1 {
		t.Fatalf("audit_chain_broken: raised %v cleared %v", raised, cleared)
	}

	// approval store: break, stop, the daemon reopens (reanchored), resume
	r2 := newRig(t)
	e2 := r2.must(r2.exporter())
	r2.request(true)
	step(t, e2)
	f, _ := os.OpenFile(filepath.Join(r2.approvalsDir, approval.FileName), os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the test's own file
	_, _ = f.WriteString(`{"v":1,"seq":99,"time":"2026-10-16T12:00:00Z","type":"expired","approval_id":"apr_x","prev_hash":"bad","hash":"bad"}` + "\n")
	_ = f.Close()
	step(t, e2)
	if _, err := approval.Open(r2.approvalsDir); err != nil {
		t.Fatal(err)
	}
	r2.now = r2.now.Add(time.Minute)
	step(t, e2)
	raised, cleared = coalescePair(t, mustAfter(t, e2), RuleApprovalChainBreak)
	if raised[0] == "" || raised[0] != cleared[0] || len(raised) != 1 || len(cleared) != 1 {
		t.Fatalf("approval_chain_break: raised %v cleared %v", raised, cleared)
	}
}

// payload_schema is the full catalog id, for every kind the exporter emits.
func TestPayloadSchemaIsTheFullCatalogIDForEveryEmittedKind(t *testing.T) {
	data, err := os.ReadFile("testdata/catalog_payload_schemas.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	delete(want, "_note")

	r := newRig(t)
	e := r.must(r.exporter())
	r.intent("op-1")
	a := r.request(true)
	if _, err := r.store.Decide(a.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Client: "terminal"}}, nil); err != nil {
		t.Fatal(err)
	}
	r.request(false) // no audit record: a gap alert after the grace
	files, _ := filepath.Glob(filepath.Join(r.auditDir, "*.jsonl"))
	f, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the test's own file
	_, _ = f.WriteString(`{"v":1,"seq":9,"id":"torn`)
	_ = f.Close()
	r.intent("op-2") // healed torn write: raised/cleared pair
	step(t, e)
	r.now = r.now.Add(2 * time.Minute)
	step(t, e)

	sink, err := OpenSchedulingSink(filepath.Join(r.root, "events"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []scheduling.Notification{{EventID: "e1", JobKey: "j", Kind: "failure"}, {EventID: "e2", JobKey: "j", Kind: "recovery"}} {
		if err := sink.Send(t.Context(), n); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	entries := append(mustAfter(t, e), mustAfterOutbox(t, sink.Outbox())...)
	for _, en := range entries {
		kind := en.Envelope.Kind
		seen[kind] = true
		if en.Envelope.Class == nil || en.Envelope.Class.PayloadSchema != want[kind] || want[kind] == "" {
			t.Errorf("%s: payload_schema %q, want %q", kind, en.Envelope.Class.PayloadSchema, want[kind])
		}
		if !strings.HasPrefix(en.Envelope.Class.PayloadSchema, "urn:hollis-labs:events:") {
			t.Errorf("%s: payload_schema %q is not a full catalog id", kind, en.Envelope.Class.PayloadSchema)
		}
	}
	for kind := range want {
		if !seen[kind] {
			t.Errorf("the scenario never produced %s", kind)
		}
	}
}

func mustAfterOutbox(t *testing.T, o Outbox) []Entry {
	t.Helper()
	entries, err := o.After(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
