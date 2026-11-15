package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
)

// The audit lane at the CLI against main's output. The goldens audit-prune.txt and
// audit-reanchor.txt were printed by main's binary (built from `git archive origin/main`) under a
// scratch HOME, on a terminal (script(1)) typing the phrase, for the chains auditFixture writes
// (CERBERUS_AUDIT_CLI_FIXTURE=<dir> keeps them, as <dir>/months/audit and <dir>/break/audit, for
// ~/.cerberus/audit). The audit directory is printed as <dir>.

type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// auditFixture writes scenario's chain in dir: "months" is two operations in each of September,
// October and November 2026; "break" three in November with the second one's operation
// rewritten under its hash.
func auditFixture(t *testing.T, dir, scenario string) {
	t.Helper()
	clk := &fixedClock{t: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
	sink, err := audit.OpenFileSinkClock(dir, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	p := audit.Principal{Kind: "agent", Surface: "socket", Via: "cli"}
	op := func() {
		in, werr := sink.Write(audit.Record{Kind: audit.KindIntent, OperationID: audit.NewID(), Connector: "local", Operation: "stop", Principal: p,
			Target: audit.Target{Kind: "local.resource", Resource: "web"}})
		if werr != nil {
			t.Fatal(werr)
		}
		if _, werr = sink.Write(audit.Record{Kind: audit.KindOutcome, OperationID: in.OperationID, Connector: "local", Operation: "stop", Principal: p, Decision: audit.DecisionAllowed}); werr != nil {
			t.Fatal(werr)
		}
	}
	months := []time.Month{9, 10, 11}
	if scenario == "break" {
		months = []time.Month{11, 11, 11}
	}
	for _, m := range months {
		clk.mu.Lock()
		clk.t = time.Date(2026, m, 10, 12, 0, 0, 0, time.UTC)
		clk.mu.Unlock()
		op()
		if scenario == "months" {
			op()
		}
	}
	if scenario != "break" {
		return
	}
	file := filepath.Join(dir, "2026-11.jsonl")
	data, err := os.ReadFile(file) //nolint:gosec // the test's own dir
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	lines[3] = strings.Replace(lines[3], `"operation":"stop"`, `"operation":"stap"`, 1)
	if err = os.WriteFile(file, []byte(strings.Join(lines, "")), 0o600); err != nil { //nolint:gosec // as above
		t.Fatal(err)
	}
}

// TestWriteAuditCLIFixture keeps the fixture chains for main's run, when asked.
func TestWriteAuditCLIFixture(t *testing.T) {
	out := os.Getenv("CERBERUS_AUDIT_CLI_FIXTURE")
	if out == "" {
		t.Skip("CERBERUS_AUDIT_CLI_FIXTURE is not set")
	}
	for _, s := range []string{"months", "break"} {
		auditFixture(t, filepath.Join(out, s, "audit"), s)
	}
}

// chainServer is the API over a gate whose sink is the audit chain in dir, served to the CLI's
// client as *as.
func chainServer(t *testing.T, dir string) (*brakeServer, *gate.Broker) {
	t.Helper()
	sink, err := audit.OpenFileSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := gate.NewBroker(sink, filepath.Join(filepath.Dir(dir), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := api.New(api.Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir(),
		Gate: gate.New(gate.Options{Sink: sink, Broker: broker})})
	if err != nil {
		t.Fatal(err)
	}
	s := &brakeServer{as: &libauth.Principal{}}
	*s.as = cliKey
	mux := a.Mux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(libauth.WithPrincipal(r.Context(), *s.as)))
	}))
	t.Cleanup(srv.Close)
	s.c = &apiClient{base: srv.URL + "/api", key: "test", keyFile: "/keys/cli.key", http: srv.Client()}
	return s, broker
}

func (s *brakeServer) runAudit(t *testing.T, terminal bool, stdin string, args ...string) (string, error) {
	t.Helper()
	group := &cobra.Command{Use: "audit"}
	group.AddCommand(auditWriteCommands(func() (*apiClient, error) { return s.c, nil }, func() bool { return terminal })...)
	out, _, err := runCLI(t, []*cobra.Command{group}, stdin, args...)
	return out, err
}

// The CLI's key prunes only with the owner's approval: off a terminal and without --approval it
// is refused as old refused it; on a terminal it shows old's plan and asks old's phrase, then the
// server asks for the owner's approval (printed, exit 0, nothing removed); once the owner
// approves on the console, the same command with --approval removes the files, printing old's
// lines. The difference from main's output is named: with --approval the phrase is not asked
// again (the owner approved exactly these files), so the prompt is not printed.
func TestAuditPruneAtTheCLIMatchesMain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	auditFixture(t, dir, "months")
	s, broker := chainServer(t, dir)
	want := strings.ReplaceAll(golden(t, "audit-prune.txt"), "<dir>", dir)
	prompt := `Type "prune before 2026-11-01" to confirm: `

	if _, err := s.runAudit(t, false, "", "audit", "prune", "--before", "2026-11-01"); err != errAuditPruneNotInteractive {
		t.Fatalf("off a terminal: %v", err)
	}
	out, err := s.runAudit(t, true, "nope\n", "audit", "prune", "--before", "2026-11-01")
	if err == nil || err.Error() != "confirmation did not match; nothing was removed" || len(broker.List()) != 0 {
		t.Fatalf("a wrong phrase: %q %v", out, err)
	}
	out, err = s.runAudit(t, true, "prune before 2026-11-01\n", "audit", "prune", "--before", "2026-11-01")
	head, _, _ := strings.Cut(want, prompt)
	if err != nil || !strings.HasPrefix(out, head+prompt) || !strings.Contains(out, "on the console, signed in as the owner, then run this again with --approval apr_") {
		t.Fatalf("the key's first prune:\n%s\n%v", out, err)
	}
	if got := len(mustGlob(t, dir)); got != 3 {
		t.Fatalf("a pending prune removed files: %d left", got)
	}
	id := broker.List()[0].ID
	s.ownerApproves(t, id)
	out, err = s.runAudit(t, false, "", "audit", "prune", "--before", "2026-11-01", "--approval", id)
	if err != nil || out != strings.Replace(want, prompt, "", 1) {
		t.Fatalf("the approved prune:\n got:\n%s\nwant (main's, without the prompt):\n%s\n%v", out, strings.Replace(want, prompt, "", 1), err)
	}
	if got := mustGlob(t, dir); len(got) != 1 {
		t.Fatalf("after the prune: %v", got)
	}
	out, err = s.runAudit(t, false, "", "audit", "prune", "--before", "2026-11-01", "--approval", id)
	if err != nil || out != "no audit month file lies wholly before 2026-11-01; nothing to prune\n" {
		t.Fatalf("nothing to prune: %q %v", out, err)
	}
}

// Reanchoring at the CLI, as pruning: old's problems and phrase, the owner's approval, then
// old's "reanchored at seq N" with --approval (no prompt, named as for prune). An MCP caller is
// refused with both owner paths named.
func TestAuditReanchorAtTheCLIMatchesMain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	auditFixture(t, dir, "break")
	s, broker := chainServer(t, dir)
	want := strings.ReplaceAll(golden(t, "audit-reanchor.txt"), "<dir>", dir)
	prompt := `Type "reanchor" to acknowledge them and trust what is written from here on: `
	head, _, _ := strings.Cut(want, prompt)

	*s.as = libauth.Principal{Verified: true, ID: api.LocalTrustID, Kind: libauth.KindOperator, Scopes: []string{api.ScopeAdmin}}
	_, err := s.runAudit(t, true, "reanchor\n", "audit", "reanchor")
	if err == nil || !strings.Contains(err.Error(), "428 approval_required") || !strings.Contains(err.Error(), "--approval <id>") {
		t.Fatalf("an MCP caller: %v", err)
	}
	*s.as = cliKey
	out, err := s.runAudit(t, true, "reanchor\n", "audit", "reanchor")
	if err != nil || !strings.HasPrefix(out, head+prompt) || len(broker.List()) != 1 {
		t.Fatalf("the key's first reanchor:\n%s\n%v", out, err)
	}
	// The approval request is itself a record, so the seq main printed is one later here.
	id := broker.List()[0].ID
	s.ownerApproves(t, id)
	out, err = s.runAudit(t, false, "", "audit", "reanchor", "--approval", id)
	if err != nil || !strings.HasPrefix(out, head) || !strings.HasPrefix(strings.TrimPrefix(out, head), "reanchored at seq ") ||
		!strings.HasSuffix(out, "; brakes are read from here on, and can be lifted again\n") {
		t.Fatalf("the approved reanchor:\n%s\n%v", out, err)
	}
	if out, err = s.runAudit(t, false, "", "audit", "reanchor", "--approval", id); err != nil || out != audit.ErrNothingToReanchor.Error()+"\n" {
		t.Fatalf("nothing to reanchor: %q %v", out, err)
	}
	if !strings.Contains(want, "reanchored at seq ") {
		t.Fatalf("main's golden: %s", want)
	}
}

func mustGlob(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}
