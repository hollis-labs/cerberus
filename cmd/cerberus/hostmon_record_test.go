package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/hostmon"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

// compactingStore is a sample store whose compaction deletes rows.
type compactingStore struct {
	hostmon.SampleStore
	rep hosts.CompactReport
}

func (s compactingStore) Compact(context.Context, time.Time, hosts.Retention) (hosts.CompactReport, error) {
	return s.rep, nil
}

// The janitor's compaction, which deletes telemetry rows, is written to the audit chain as the
// old build wrote it: an automation monitor_event on the host connector, through the gate,
// with what it removed as the outcome's reason (old's call.note). A lockdown does not stop it (automation is recorded, never
// refused), and without a gate (observe mode) nothing is recorded.
func TestTheJanitorsCompactionIsRecordedInTheAuditChain(t *testing.T) {
	w := gatetest.New(t, gatetest.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rep := hosts.CompactReport{RollupsWritten: 2, RawDeleted: 40, Rollup5mDeleted: 3}
	compact := func(rec hostmon.Recorder) {
		m := hostmon.New(hostmon.Options{Store: compactingStore{rep: rep}, Telemetry: hostmon.NewTelemetry(0, logger),
			Record: rec, Logger: logger, Now: time.Now})
		m.CompactOnce(context.Background())
	}

	compact(hostMonitorRecorder(w.Gate, logger))
	w.Engage(t, brake.State{Lockdown: &brake.Lockdown{ID: "lk"}})
	compact(hostMonitorRecorder(w.Gate, logger))

	var intents, outcomes []audit.Record
	for _, r := range w.Sink.Records() {
		if r.Connector != "host" || r.Operation != "monitor_event" {
			continue
		}
		switch r.Kind {
		case audit.KindIntent:
			intents = append(intents, r)
		case audit.KindOutcome:
			outcomes = append(outcomes, r)
		}
	}
	if len(intents) != 2 || len(outcomes) != 2 {
		t.Fatalf("records: %d intents, %d outcomes, want 2 of each (one per compaction, the lockdown's included)", len(intents), len(outcomes))
	}
	for _, r := range outcomes {
		if r.Principal.Kind != audit.PrincipalAutomation || r.Decision == "refused" ||
			!strings.Contains(r.Reason, "compacted: "+rep.String()) {
			t.Fatalf("the compaction's record: principal %q, decision %q, reason %q", r.Principal.Kind, r.Decision, r.Reason)
		}
	}

	if hostMonitorRecorder(nil, logger) != nil {
		t.Fatal("a recorder without a gate (observe mode)")
	}
}
