package eventexport

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/ingest"
)

func lifecycleOutcome(op, code string, p audit.Principal) audit.Record {
	return audit.Record{
		Seq: 7, Hash: "abcdef0123456789", Kind: audit.KindOutcome, OperationID: "op_1",
		Time: time.Date(2026, 10, 20, 12, 0, 10, 0, time.UTC), DurationMS: 4000,
		Principal: p, Connector: "local", Operation: op, OutcomeCode: code, Decision: audit.DecisionAllowed,
		Target: audit.Target{Kind: "resource", Fields: map[string]string{"id": "web"}, Resource: "web", Env: "dev", Owner: "self", Admin: "self"},
	}
}

type lifeEvent struct {
	kind, to, from, reason, trigger string
}

func decodeLife(t *testing.T, ms []Mapped) []lifeEvent {
	t.Helper()
	var out []lifeEvent
	for _, m := range ms {
		var p struct {
			To, From, Reason string
			Trigger          struct{ Kind string }
		}
		if err := json.Unmarshal(m.Envelope.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, lifeEvent{string(m.Envelope.Kind), p.To, p.From, p.Reason, p.Trigger.Kind})
	}
	return out
}

func TestMapLifecycleOperations(t *testing.T) {
	human := audit.Principal{Kind: "human", Surface: "socket"}
	auto := audit.Principal{Kind: audit.PrincipalAutomation, Surface: "monitor"}
	sched := audit.Principal{Kind: "human", Surface: "scheduler"}
	cases := []struct {
		name string
		rec  audit.Record
		want []lifeEvent
	}{
		{"stop by a person", lifecycleOutcome("stop", "ok", human),
			[]lifeEvent{{"resource.state_changed", "stopped", "", "operator_stop", "human"}}},
		{"stop by automation has no operator reason", lifecycleOutcome("stop", "ok", auto),
			[]lifeEvent{{"resource.state_changed", "stopped", "", "", "system"}}},
		{"apply", lifecycleOutcome("apply", "ok", human),
			[]lifeEvent{{"resource.state_changed", "running", "", "", "human"}}},
		{"reload by the monitor", lifecycleOutcome("reload", "ok", auto),
			[]lifeEvent{{"resource.state_changed", "running", "", "restart", "system"}}},
		{"reload by the scheduler", lifecycleOutcome("reload", "ok", sched),
			[]lifeEvent{{"resource.state_changed", "running", "", "restart", "scheduler"}}},
		{"remove", lifecycleOutcome("remove", "ok", human),
			[]lifeEvent{{"resource.state_changed", "removed", "", "", "human"}}},
		{"apply that failed", lifecycleOutcome("apply", "operation_failed", human),
			[]lifeEvent{{"resource.state_changed", "failed", "", "", "human"}}},
		{"deploy ok", lifecycleOutcome("deploy", "ok", human), []lifeEvent{
			{"deploy.state_changed", "started", "", "", "human"},
			{"resource.state_changed", "deploying", "", "deploy", "human"},
			{"deploy.state_changed", "succeeded", "started", "", "human"},
			{"resource.state_changed", "running", "deploying", "deploy", "human"},
		}},
		{"deploy failed", lifecycleOutcome("deploy", "operation_failed", human), []lifeEvent{
			{"deploy.state_changed", "started", "", "", "human"},
			{"resource.state_changed", "deploying", "", "deploy", "human"},
			{"deploy.state_changed", "failed", "started", "", "human"},
			{"resource.state_changed", "failed", "deploying", "deploy", "human"},
		}},
		// Nothing exported: a failed stop or remove leaves the state unknown, sync and
		// reads change no lifecycle state.
		{"failed stop", lifecycleOutcome("stop", "operation_failed", human), nil},
		{"failed remove", lifecycleOutcome("remove", "operation_failed", human), nil},
		{"sync", lifecycleOutcome("sync", "ok", human), nil},
		{"status read", lifecycleOutcome("status", "ok", human), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decodeLife(t, MapLifecycle(c.rec))
			if len(got) != len(c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("event %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestMapLifecycleSkipsWhatDidNotRun(t *testing.T) {
	human := audit.Principal{Kind: "human", Surface: "socket"}
	for name, mod := range map[string]func(*audit.Record){
		"intent":      func(r *audit.Record) { r.Kind = audit.KindIntent },
		"refused":     func(r *audit.Record) { r.Decision = audit.DecisionRefused; r.OutcomeCode = "approval_required" },
		"dry run":     func(r *audit.Record) { r.DryRun = true },
		"other conn.": func(r *audit.Record) { r.Connector = "ssh" },
		"no resource": func(r *audit.Record) { r.Target = audit.Target{} },
		"no op id":    func(r *audit.Record) { r.OperationID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			rec := lifecycleOutcome("deploy", "ok", human)
			mod(&rec)
			if got := MapLifecycle(rec); got != nil {
				t.Fatalf("mapped %d events", len(got))
			}
		})
	}
}

// A deploy's four events tie together by correlation id, the started pair carries
// the operation's true start time, and keys are unique and stable so a replay
// appends nothing twice.
func TestMapLifecycleDeployTimesCorrelationAndKeys(t *testing.T) {
	rec := lifecycleOutcome("deploy", "ok", audit.Principal{Kind: "human", Surface: "socket"})
	ms := MapLifecycle(rec)
	again := MapLifecycle(rec)
	seen := map[string]bool{}
	for i, m := range ms {
		if m.Key != again[i].Key {
			t.Fatalf("key %d not stable: %s vs %s", i, m.Key, again[i].Key)
		}
		if seen[m.Key] {
			t.Fatalf("duplicate key %s", m.Key)
		}
		seen[m.Key] = true
		if m.Envelope.Corr == nil || m.Envelope.Corr.CorrelationID != "op_1" {
			t.Fatalf("event %d: correlation = %+v", i, m.Envelope.Corr)
		}
		wantAt := rec.Time.Add(-4 * time.Second)
		if i >= 2 {
			wantAt = rec.Time
		}
		if !m.Envelope.Time.Equal(wantAt) {
			t.Fatalf("event %d time = %v, want %v", i, m.Envelope.Time, wantAt)
		}
	}
	if ms[0].Envelope.Subject != "msg://deployment/cerberus/op_1" || ms[1].Envelope.Subject != "msg://resource/cerberus/web" {
		t.Fatalf("subjects = %s, %s", ms[0].Envelope.Subject, ms[1].Envelope.Subject)
	}
}

// What leaves the process must pass the hub's own shape and catalog checks, carry
// only labels from the allowlist, and hold no free text from the record.
func TestMapLifecycleEnvelopesPassIngest(t *testing.T) {
	box, err := OpenFileOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec := lifecycleOutcome("deploy", "ok", audit.Principal{Kind: "human", Surface: "socket"})
	rec.Reason = "secret-looking free text must never travel"
	for _, m := range MapLifecycle(rec) {
		entry, _, err := box.Append(m.Key, SourceRef{Source: "audit", Seq: rec.Seq}, m.Envelope)
		if err != nil {
			t.Fatal(err)
		}
		env := entry.Envelope
		env.Actor = nil // the hub fills the actor; a producer never sends one
		raw, _ := json.Marshal(env)
		if _, rej := ingest.CheckItem("cerberus", raw, ingest.Options{}); rej != nil {
			t.Fatalf("%s: %v", m.Envelope.Kind, rej)
		}
		if strings.Contains(string(raw), "secret-looking") {
			t.Fatalf("free text leaked: %s", raw)
		}
		var p struct{ Labels map[string]string }
		_ = json.Unmarshal(env.Payload, &p)
		for k := range p.Labels {
			switch k {
			case "project", "host", "connector", "mode", "supervisor", "run_from", "env", "owner", "admin":
			default:
				t.Fatalf("label %q is not in the allowlist", k)
			}
		}
	}
}

// End to end: a deploy written to a real audit chain comes out of the exporter
// as its audit.recorded records plus the lifecycle events, once, across a restart.
func TestExporterTurnsAnAuditedDeployIntoLifecycleEventsOnce(t *testing.T) {
	r := newRig(t)
	e := r.must(r.exporter())
	step(t, e) // first enable: start at the tail

	target := audit.Target{Kind: "resource", Fields: map[string]string{"id": "web"}, Resource: "web", Env: "dev"}
	human := audit.Principal{Kind: "human", Surface: "socket"}
	for _, rec := range []audit.Record{
		{Kind: audit.KindIntent, OperationID: "op_9", Principal: human, Connector: "local", Operation: "deploy", Target: target},
		{Kind: audit.KindOutcome, OperationID: "op_9", Principal: human, Connector: "local", Operation: "deploy", Target: target,
			OutcomeCode: audit.OutcomeOK, Decision: audit.DecisionAllowed, DurationMS: 1500},
	} {
		if _, err := r.sink.Write(rec); err != nil {
			t.Fatal(err)
		}
	}
	step(t, e)
	want := "audit.recorded,audit.recorded,deploy.state_changed,resource.state_changed,deploy.state_changed,resource.state_changed"
	if got := strings.Join(kinds(t, e), ","); got != want {
		t.Fatalf("kinds = %s, want %s", got, want)
	}

	again := r.must(r.exporter()) // a restart replays nothing
	step(t, again)
	if got := strings.Join(kinds(t, again), ","); got != want {
		t.Fatalf("after restart kinds = %s", got)
	}
}
