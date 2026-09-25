package audit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// threeMonths writes one record into each of July, August and September and
// returns a sink whose clock sits in September.
func threeMonths(t *testing.T, dir string) *FileSink {
	t.Helper()
	var sink *FileSink
	for _, m := range []time.Month{time.July, time.August, time.September} {
		var err error
		sink, err = openFileSink(dir, clock(time.Date(2026, m, 10, 0, 0, 0, 0, time.UTC)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sink.Write(Record{Kind: KindIntent, Connector: "ssh", Operation: "exec"}); err != nil {
			t.Fatal(err)
		}
	}
	return sink
}

func TestPruneRemovesWholeMonthsAndStaysVerifiable(t *testing.T) {
	dir := t.TempDir()
	sink := threeMonths(t, dir)

	// Mid-August: only July lies wholly before it.
	plan, err := PrunePlan(dir, time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC))
	if err != nil || len(plan) != 1 || filepath.Base(plan[0]) != "2026-07.jsonl" {
		t.Fatalf("plan %v, %v", plan, err)
	}
	// Far in the future: the newest file is never in the plan.
	plan, _ = PrunePlan(dir, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(plan) != 2 {
		t.Fatalf("plan %v, want July and August only", plan)
	}

	removed, err := Prune(sink, dir, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Principal{Surface: "in_process", SelfReported: true})
	if err != nil || strings.Join(removed, ",") != "2026-07.jsonl,2026-08.jsonl" {
		t.Fatalf("removed %v, %v", removed, err)
	}
	if err := Verify(dir); err != nil {
		t.Fatalf("a recorded prune fails verification: %v", err)
	}
	recs := readRecords(t, filepath.Join(dir, "2026-09.jsonl"))
	outcome := recs[len(recs)-1]
	if outcome.Kind != KindOutcome || outcome.Operation != PruneOperation || outcome.Effect != "admin" ||
		outcome.Target.Fields["removed"] != "2026-07.jsonl,2026-08.jsonl" {
		t.Fatalf("the prune is not recorded: %+v", outcome)
	}
	if _, err := Prune(sink, dir, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Principal{}); !errors.Is(err, ErrNothingToPrune) {
		t.Fatalf("second prune: %v", err)
	}
}

// Month files removed without a recorded prune are a break.
func TestVerifyRejectsUnrecordedRemoval(t *testing.T) {
	dir := t.TempDir()
	threeMonths(t, dir)
	if err := os.Remove(filepath.Join(dir, "2026-07.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir); err == nil || !strings.Contains(err.Error(), "no recorded prune removed") {
		t.Fatalf("err = %v", err)
	}
	// A hole in the middle is a break too.
	dir = t.TempDir()
	threeMonths(t, dir)
	if err := os.Remove(filepath.Join(dir, "2026-08.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir); err == nil {
		t.Fatal("a missing middle month verified")
	}
}

// An unwritable log refuses the prune before anything is removed.
func TestPruneRefusesWhenItCannotBeRecorded(t *testing.T) {
	dir := t.TempDir()
	threeMonths(t, dir)
	if _, err := Prune(Failing{}, dir, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Principal{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-07.jsonl")); err != nil {
		t.Fatal("an unrecorded prune removed a file")
	}
}

func TestFilterMatches(t *testing.T) {
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rec := Record{Kind: KindOutcome, Time: at, Connector: "local", Operation: "deploy",
		Principal: Principal{Surface: "socket"}, Target: Target{Fields: map[string]string{"id": "api"}},
		Decision: DecisionRefused, OutcomeCode: "acknowledgment_required"}
	for name, c := range map[string]struct {
		f    Filter
		want bool
	}{
		"empty":         {Filter{}, true},
		"all":           {Filter{Connector: "local", Operation: "deploy", Surface: "socket", Target: "api", Outcome: "refused"}, true},
		"code":          {Filter{Outcome: "acknowledgment_required"}, true},
		"other code":    {Filter{Outcome: "ok"}, false},
		"connector":     {Filter{Connector: "ssh"}, false},
		"target":        {Filter{Target: "web"}, false},
		"since":         {Filter{Since: at.Add(time.Second)}, false},
		"until":         {Filter{Until: at}, false},
		"window":        {Filter{Since: at, Until: at.Add(time.Second)}, true},
		"intent no out": {Filter{Outcome: "refused"}, true},
	} {
		if got := c.f.Match(rec); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	intent := rec
	intent.Kind = KindIntent
	if (Filter{Outcome: "refused"}).Match(intent) {
		t.Error("an outcome filter matched an intent")
	}
}
