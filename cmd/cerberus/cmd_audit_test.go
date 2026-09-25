package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// auditFixture is an audit directory whose chain spans an old month file and
// the current one, and points the audit commands at it.
func auditFixture(t *testing.T, terminal bool) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "audit")
	sink, err := audit.OpenFileSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	write := func(s audit.Sink, rec audit.Record) {
		if _, writeErr := s.Write(rec); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	write(sink, audit.Record{Kind: audit.KindIntent, Connector: "ssh", Operation: "exec", Principal: audit.Principal{Surface: "socket"}})
	write(sink, audit.Record{Kind: audit.KindOutcome, Connector: "ssh", Operation: "exec", Principal: audit.Principal{Surface: "socket"},
		Decision: audit.DecisionRefused, OutcomeCode: "acknowledgment_required", Target: audit.Target{Kind: "host", Fields: map[string]string{"resource": "muctl"}}})
	current := filepath.Join(dir, time.Now().UTC().Format("2006-01")+".jsonl")
	if err = os.Rename(current, filepath.Join(dir, "2000-01.jsonl")); err != nil {
		t.Fatal(err)
	}
	sink, err = audit.OpenFileSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	write(sink, audit.Record{Kind: audit.KindOutcome, Connector: "local", Operation: "deploy", Principal: audit.Principal{Surface: "in_process"},
		Decision: audit.DecisionAllowed, OutcomeCode: audit.OutcomeOK, Target: audit.Target{Kind: "resource", Fields: map[string]string{"id": "api"}}})

	oldDir, oldSink, oldTerm := auditDir, auditSink, auditIsTerminal
	auditDir = func() (string, error) { return dir, nil }
	auditSink = func() audit.Sink { return sink }
	auditIsTerminal = func() bool { return terminal }
	t.Cleanup(func() { auditDir, auditSink, auditIsTerminal = oldDir, oldSink, oldTerm })
	return dir
}

func runAudit(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	// Flag values outlive an Execute; start each run from the defaults.
	auditOutput, auditTailCount, auditPruneBefore = outputFormatText, 20, ""
	auditQuery.connector, auditQuery.operation, auditQuery.surface = "", "", ""
	auditQuery.outcome, auditQuery.target, auditQuery.since, auditQuery.until = "", "", "", ""
	auditQuery.limit = 0
	var out bytes.Buffer
	rootCmd.SetArgs(append([]string{"audit"}, args...))
	rootCmd.SetOut(&out)
	rootCmd.SetIn(strings.NewReader(stdin))
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetIn(nil)
	})
	err := rootCmd.Execute()
	return out.String(), err
}

func TestAuditTailAndQuery(t *testing.T) {
	auditFixture(t, false)

	out, err := runAudit(t, "", "tail", "-n", "2")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "local deploy") || !strings.Contains(lines[1], "resource(id=api)") {
		t.Fatalf("tail:\n%s", out)
	}

	out, err = runAudit(t, "", "query", "--connector", "ssh", "--outcome", "refused", "--target", "muctl", "--surface", "socket", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rec audit.Record
	if err = json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil || rec.OutcomeCode != "acknowledgment_required" {
		t.Fatalf("query -o json gave %q (%v)", out, err)
	}

	out, err = runAudit(t, "", "query", "--op", "deploy", "--since", "2000-02-01", "--until", "2999-01-01")
	if err != nil || strings.Count(out, "\n") != 1 {
		t.Fatalf("query window:\n%s (%v)", out, err)
	}
	if _, err := runAudit(t, "", "query", "--since", "yesterday"); err == nil {
		t.Fatal("an unparseable --since was accepted")
	}
}

func TestAuditVerify(t *testing.T) {
	dir := auditFixture(t, false)
	out, err := runAudit(t, "", "verify")
	if err != nil || !strings.Contains(out, "audit chain intact: 5 records in 2 month file(s)") {
		t.Fatalf("verify: %q (%v)", out, err)
	}
	// Removing the old month without a recorded prune is a break.
	if err := os.Remove(filepath.Join(dir, "2000-01.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := runAudit(t, "", "verify"); err == nil {
		t.Fatal("verify passed a chain with a month file missing")
	}
}

func TestAuditPrune(t *testing.T) {
	t.Run("refuses a non-terminal", func(t *testing.T) {
		dir := auditFixture(t, false)
		_, err := runAudit(t, "prune before 2000-02-01\n", "prune", "--before", "2000-02-01")
		if !errors.Is(err, errAuditPruneNotInteractive) {
			t.Fatalf("err = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "2000-01.jsonl")); statErr != nil {
			t.Fatal("a non-interactive prune removed a file")
		}
		// The refusal names what to do, and main's redaction leaves it whole.
		if got := redact.Text(err.Error()); got != err.Error() {
			t.Fatalf("redaction rewrote the refusal: %q", got)
		}
	})
	t.Run("refuses a wrong confirmation", func(t *testing.T) {
		dir := auditFixture(t, true)
		if _, err := runAudit(t, "yes\n", "prune", "--before", "2000-02-01"); err == nil {
			t.Fatal("a wrong confirmation pruned")
		}
		if _, err := os.Stat(filepath.Join(dir, "2000-01.jsonl")); err != nil {
			t.Fatal("a wrong confirmation removed a file")
		}
	})
	t.Run("removes whole months, is recorded, and verifies", func(t *testing.T) {
		dir := auditFixture(t, true)
		out, err := runAudit(t, "prune before 2000-02-01\n", "prune", "--before", "2000-02-01")
		if err != nil || !strings.Contains(out, "removed 2000-01.jsonl") {
			t.Fatalf("prune: %q (%v)", out, err)
		}
		if got := redact.Text(auditPrunePhrase(time.Date(2000, 2, 1, 0, 0, 0, 0, time.UTC))); got != "prune before 2000-02-01" {
			t.Fatalf("redaction rewrote the confirmation phrase: %q", got)
		}
		if _, err = os.Stat(filepath.Join(dir, "2000-01.jsonl")); !os.IsNotExist(err) {
			t.Fatal("the old month is still there")
		}
		if _, err = runAudit(t, "", "verify"); err != nil {
			t.Fatalf("verify after a recorded prune: %v", err)
		}
		out, err = runAudit(t, "", "query", "--connector", "audit", "--op", "prune", "--outcome", "ok")
		if err != nil || !strings.Contains(out, "removed=2000-01.jsonl") {
			t.Fatalf("the prune is not in the log:\n%s (%v)", out, err)
		}
		// Nothing left before that date: it says so and removes nothing.
		out, err = runAudit(t, "prune before 2000-02-01\n", "prune", "--before", "2000-02-01")
		if err != nil || !strings.Contains(out, "nothing to prune") {
			t.Fatalf("second prune: %q (%v)", out, err)
		}
	})
	t.Run("an unwritable log removes nothing", func(t *testing.T) {
		dir := auditFixture(t, true)
		auditSink = func() audit.Sink { return audit.Failing{} }
		if _, err := runAudit(t, "prune before 2000-02-01\n", "prune", "--before", "2000-02-01"); err == nil {
			t.Fatal("an unrecordable prune succeeded")
		}
		if _, err := os.Stat(filepath.Join(dir, "2000-01.jsonl")); err != nil {
			t.Fatal("an unrecordable prune removed a file")
		}
	})
}
