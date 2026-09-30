package brake

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// appendRaw writes ev to the store as a same-uid process could: chained
// correctly, with whatever it likes in it.
func appendRaw(t *testing.T, s Store, ev Event) {
	t.Helper()
	f := s.fold()
	if ev.V == 0 {
		ev.V = eventVersion
	}
	ev.Seq, ev.Time, ev.PrevHash = f.seq+1, time.Now().UTC(), f.last
	ev.Hash = hashEvent(ev)
	line, _ := json.Marshal(ev)
	file, err := os.OpenFile(s.path(), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close() //nolint:errcheck
	if _, err := file.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

// A lift or reset that carries no proof is not applied, however well it
// chains (M1).
func TestAProoflessLiftIsNotApplied(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	_, l, _ := s.EngageLockdown(operator, "incident")
	_, f, _ := s.EngageFreeze(policy.TargetMatch{ID: "api"}, operator, "")
	_, x, _ := s.Suspend("agent|mcp_stdio|session:s1", operator, 5, "10m")
	for _, ev := range []Event{
		{Type: EventLockdownLifted, ID: l.ID},
		{Type: EventFreezeLifted, ID: f.ID},
		{Type: EventSuspensionReset, ID: x.ID},
	} {
		appendRaw(t, s, ev)
	}
	st, problems := s.Load()
	if st.Lockdown == nil || len(st.Freezes) != 1 || len(st.Suspensions) != 1 || len(problems) != 3 {
		t.Fatalf("state %+v, problems %v", st, problems)
	}
	if _, err := s.LiftLockdown(l.ID, operator, ""); err == nil {
		t.Fatal("the store appended a lift without its proof")
	}
}

// A suspension reset written before resets carried a proof still applies,
// so upgrading does not bring back a suspension a person reset.
func TestALegacyResetStillApplies(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	_, x, _ := s.Suspend("agent|mcp_stdio|session:s1", operator, 5, "10m")
	appendRaw(t, s, Event{V: 1, Type: EventSuspensionReset, ID: x.ID})
	if st, problems := s.Load(); len(st.Suspensions) != 0 || len(problems) != 0 {
		t.Fatalf("a legacy reset: %+v (%v)", st, problems)
	}
}

// Past a break in the store's chain, what engages a brake is applied and
// what lifts one is not, until an append reanchors it.
func TestAStoreBreakLetsOnlyEngagementsThrough(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	_, l, _ := s.EngageLockdown(operator, "incident")
	replaceIn(t, s.path(), "incident", "accident")
	appendRaw(t, s, Event{Type: EventLockdownLifted, ID: l.ID, Proof: "tty"})
	appendRaw(t, s, Event{Type: EventFreezeEngaged, Freeze: &Freeze{ID: "frz_1", Match: policy.TargetMatch{ID: "api"}}})
	st, problems := s.Load()
	if st.Lockdown == nil || len(st.Freezes) != 1 {
		t.Fatalf("past the break: %+v (%v)", st, problems)
	}
	// A person's lift reanchors the store first, so it applies, and keeps
	// applying on the next load.
	if _, err := s.LiftLockdown(l.ID, operator, "tty"); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.Load(); st.Lockdown != nil || len(st.Freezes) != 1 {
		t.Fatalf("after the reanchored lift: %+v", st)
	}
}

func recordBrakes(t *testing.T, sink audit.Sink, st State) {
	t.Helper()
	data, _ := json.Marshal(st)
	if _, err := sink.Write(audit.Record{Kind: audit.KindBrakeChanged, Brakes: data}); err != nil {
		t.Fatal(err)
	}
}

// A broken audit chain no longer drops the log's brakes: a brake engaged
// anywhere past the break stays engaged, a lift recorded there is not
// applied, and after a reanchor the log is read as usual (M1).
func TestRecordedFailsClosedPastAnAuditBreak(t *testing.T) {
	auditDir := filepath.Join(t.TempDir(), "audit")
	sink, err := audit.OpenFileSink(auditDir)
	if err != nil {
		t.Fatal(err)
	}
	lockdown := State{Lockdown: &Lockdown{ID: "ldn_1", By: operator}}
	recordBrakes(t, sink, lockdown)
	recordBrakes(t, sink, State{Freezes: []Freeze{{ID: "frz_1", Match: policy.TargetMatch{ID: "api"}}}})
	// Break the chain in the first brake record, and write a lift of
	// everything after it.
	replaceIn(t, monthFile(t, auditDir), `"ldn_1"`, `"ldn_2"`)
	recordBrakes(t, sink, State{})
	st, problems := Recorded(auditDir)
	if st.Lockdown == nil || len(st.Freezes) != 1 || len(problems) != 1 || !strings.Contains(problems[0], "cerberus audit reanchor") {
		t.Fatalf("past the break: %+v (%v)", st, problems)
	}
	if _, err := audit.Reanchor(sink, auditDir, operator); err != nil {
		t.Fatal(err)
	}
	recordBrakes(t, sink, State{})
	if st, problems = Recorded(auditDir); st.Engaged() || len(problems) != 1 || !strings.Contains(problems[0], "before its last reanchor") {
		t.Fatalf("after the reanchor: %+v (%v)", st, problems)
	}
}

// replaceIn edits a file as a same-uid process could.
func replaceIn(t *testing.T, file, from, to string) {
	t.Helper()
	data, err := os.ReadFile(file) //nolint:gosec // the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, bytes.Replace(data, []byte(from), []byte(to), 1), 0o600); err != nil { //nolint:gosec // as above
		t.Fatal(err)
	}
}

func monthFile(t *testing.T, dir string) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("month files %v", files)
	}
	return files[0]
}

// A brake the audit log holds and the store lost is written back, so it
// can be lifted as usual.
func TestRestoreWritesBackWhatTheStoreLost(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	recorded := State{
		Lockdown:    &Lockdown{ID: "ldn_1", By: operator},
		Freezes:     []Freeze{{ID: "frz_1", Match: policy.TargetMatch{ID: "api"}}},
		Suspensions: []Suspension{{ID: "sus_1", Key: "agent|x"}},
	}
	st, err := s.Restore(recorded)
	if err != nil || st.Lockdown == nil || st.Lockdown.ID != "ldn_1" || len(st.Freezes) != 1 || len(st.Suspensions) != 1 {
		t.Fatalf("restored %+v, %v", st, err)
	}
	if st, _ = s.Restore(recorded); len(st.Freezes) != 1 || len(st.Suspensions) != 1 {
		t.Fatalf("a second restore duplicated: %+v", st)
	}
	if st, err = s.LiftFreeze("frz_1", operator, "tty"); err != nil || len(st.Freezes) != 0 {
		t.Fatalf("lift of a restored freeze: %+v, %v", st, err)
	}
}

// An event carrying a name that is not valid UTF-8 chains: it is checked
// against the bytes it was hashed as (M2).
func TestAnEventWithInvalidUTF8Chains(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if _, _, err := s.EngageLockdown(audit.Principal{Kind: "agent", Client: "clipped \xc3"}, "incident"); err != nil {
		t.Fatal(err)
	}
	if _, problems := s.Load(); len(problems) != 0 {
		t.Fatalf("problems %v", problems)
	}
}

// The re-review's repro (H-e): one line over the reader's limit made the
// log unreadable, so the brakes were read from their store alone, and
// deleting the store lifted a lockdown the log recorded. The log is now
// read past the line: the lockdown stays, and past the line only
// engagements apply.
func TestAnOverlongAuditLineDoesNotLiftABrake(t *testing.T) {
	auditDir := filepath.Join(t.TempDir(), "audit")
	sink, err := audit.OpenFileSink(auditDir)
	if err != nil {
		t.Fatal(err)
	}
	recordBrakes(t, sink, State{Lockdown: &Lockdown{ID: "ldn_1", By: operator}})
	f, err := os.OpenFile(monthFile(t, auditDir), os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"v":1,"note":"` + strings.Repeat("<", audit.MaxLineBytes) + `"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	recordBrakes(t, sink, State{})
	st, problems := Recorded(auditDir)
	if st.Lockdown == nil {
		t.Fatalf("the lockdown was lifted past an overlong line: %v", problems)
	}
	for _, p := range problems {
		if strings.Contains(p, "store alone") {
			t.Fatalf("the brakes fell back to the store: %v", problems)
		}
	}
}

// The brake store is read past an overlong line too: it is a break, so an
// engagement after it applies and a lift after it does not.
func TestAnOverlongStoreLineIsABreak(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	_, l, _ := s.EngageLockdown(operator, "incident")
	f, err := os.OpenFile(s.path(), os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(strings.Repeat("x", audit.MaxLineBytes+10) + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	appendRaw(t, s, Event{Type: EventLockdownLifted, ID: l.ID, Proof: "tty"})
	appendRaw(t, s, Event{Type: EventFreezeEngaged, Freeze: &Freeze{ID: "frz_1", Match: policy.TargetMatch{ID: "api"}}})
	st, problems := s.Load()
	if st.Lockdown == nil || len(st.Freezes) != 1 || !strings.Contains(strings.Join(problems, " "), "is not read") {
		t.Fatalf("past an overlong line: %+v %v", st, problems)
	}
}
