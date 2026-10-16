package eventexport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/scheduling"
)

func TestSchedulingSinkWritesFailuresAndRecoveriesAsAlertsOncePerNotification(t *testing.T) {
	dir := t.TempDir()
	sink, openErr := OpenSchedulingSink(dir)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if !sink.Idempotent() {
		t.Fatal("the sink dedupes by EventID, so it is idempotent")
	}
	failure := scheduling.Notification{EventID: "outcome/fire-1", JobKey: "nightly", Incarnation: 2, EpisodeID: "ep-1", FireID: "fire-1", Kind: "failure"}
	recovery := scheduling.Notification{EventID: "outcome/fire-2", JobKey: "nightly", Incarnation: 2, EpisodeID: "ep-1", FireID: "fire-2", Kind: "recovery"}
	for range 3 { // ambiguous failures make the scheduler send again
		if sendErr := sink.Send(t.Context(), failure); sendErr != nil {
			t.Fatal(sendErr)
		}
	}
	if sendErr := sink.Send(t.Context(), recovery); sendErr != nil {
		t.Fatal(sendErr)
	}
	if sendErr := sink.Send(t.Context(), scheduling.Notification{EventID: "outcome/fire-3", JobKey: "nightly", Kind: "misfire"}); sendErr != nil {
		t.Fatal(sendErr)
	}
	entries, err := sink.Outbox().After(0, 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	raised, cleared := entries[0].Envelope, entries[1].Envelope
	if raised.Kind != "alert.raised" || cleared.Kind != "alert.cleared" || raised.Source.Channel != "scheduling" || raised.Subject != "msg://schedule_job/cerberus/nightly" {
		t.Fatalf("envelopes = %+v / %+v", raised, cleared)
	}
	payload := string(raised.Payload)
	for _, want := range []string{RuleScheduleJobFailed, `"source_event_ids":[]`, `"job_key":"nightly"`, `"episode_id":"ep-1"`, `"fire_id":"fire-1"`} {
		if !strings.Contains(payload, want) {
			t.Errorf("payload %s lacks %s", payload, want)
		}
	}
	if raised.Corr == nil || raised.Corr.CorrelationID != "ep-1" || raised.Class.Visibility != VisibilityEnvironment {
		t.Fatalf("corr/class = %+v / %+v", raised.Corr, raised.Class)
	}

	// A restart remembers every EventID, and its ids do not change.
	again, reopenErr := OpenSchedulingSink(dir)
	if reopenErr != nil {
		t.Fatal(reopenErr)
	}
	if sendErr := again.Send(t.Context(), failure); sendErr != nil {
		t.Fatal(sendErr)
	}
	after, _ := again.Outbox().After(0, 0)
	if len(after) != 2 || after[0].Envelope.ID != entries[0].Envelope.ID {
		t.Fatalf("after restart: %+v", after)
	}
	// Its namespace is its own: not the audit channel's.
	audit, auditErr := OpenFileOutbox(filepath.Join(t.TempDir(), "audit"))
	if auditErr != nil {
		t.Fatal(auditErr)
	}
	if audit.Generation() == again.Outbox().Generation() {
		t.Fatal("two channels share a generation")
	}
}

func TestSettingsDefaultToDisabled(t *testing.T) {
	s, err := LoadSettings(filepath.Join(t.TempDir(), "events.yaml"))
	if err != nil || s.Export.Enabled || s.Export.Grace().Seconds() != 60 || s.Export.Interval().Seconds() != 5 {
		t.Fatalf("defaults = %+v, %v", s, err)
	}
	path := filepath.Join(t.TempDir(), "events.yaml")
	if writeErr := os.WriteFile(path, []byte("export:\n  enabled: true\n  grace_seconds: 90\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	s, err = LoadSettings(path)
	if err != nil || !s.Export.Enabled || s.Export.GraceSeconds != 90 || s.Export.IntervalSeconds != 5 {
		t.Fatalf("settings = %+v, %v", s, err)
	}
	if writeErr := os.WriteFile(path, []byte("export: [not a map"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, err := LoadSettings(path); err == nil {
		t.Fatal("a broken settings file was accepted")
	}
}
