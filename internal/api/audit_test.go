package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
)

// clock is a settable time for a sink that writes across months.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

// chainAPI is the API over a gate whose sink is a real audit chain in dir, anchored in the
// (mocked) keychain as serve anchors it (auditchain.Open), on clk.
func chainAPI(t *testing.T, dir string, clk *clock) (*gatedAPI, *audit.FileSink) {
	t.Helper()
	audit.SetAnchor(dir, auditchain.KeychainAnchor{Dir: dir})
	sink, err := audit.OpenFileSinkClock(dir, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := gate.NewBroker(sink, filepath.Join(filepath.Dir(dir), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	g := gate.New(gate.Options{Sink: sink, Broker: broker})
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir(), Gate: g})
	if err != nil {
		t.Fatal(err)
	}
	return &gatedAPI{h: a.Mux()}, sink
}

// writeOps writes n intent/outcome pairs, as the lanes record an operation.
func writeOps(t *testing.T, sink audit.Sink, n int) {
	t.Helper()
	p := audit.Principal{Kind: "agent", Surface: "socket", Via: "cli"}
	for i := 0; i < n; i++ {
		in, err := sink.Write(audit.Record{Kind: audit.KindIntent, OperationID: audit.NewID(), Connector: "local", Operation: "stop", Principal: p,
			Target: audit.Target{Kind: "local.resource", Resource: "web"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = sink.Write(audit.Record{Kind: audit.KindOutcome, OperationID: in.OperationID, Connector: "local", Operation: "stop", Principal: p, Decision: audit.DecisionAllowed}); err != nil {
			t.Fatal(err)
		}
	}
}

// approveAs asks for the owner's approval as the key, has the owner approve it on the console, and
// returns its id.
func (g *gatedAPI) approveAs(t *testing.T, path, body string) string {
	t.Helper()
	id := g.pendingID(t, apiKey, "POST", path, body)
	if code, out := g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != http.StatusOK {
		t.Fatalf("the owner approving %s: %d %s", id, code, out)
	}
	return id
}

func monthFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range files {
		files[i] = filepath.Base(f)
	}
	return files
}

// pruneChain is a chain over September to November 2026, written by sink on clk.
func pruneChain(t *testing.T, dir string) (*gatedAPI, *audit.FileSink, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
	g, sink := chainAPI(t, dir, clk)
	for _, m := range []time.Month{9, 10, 11} {
		clk.set(time.Date(2026, m, 10, 12, 0, 0, 0, time.UTC))
		writeOps(t, sink, 2)
	}
	return g, sink, clk
}

// audit prune is the owner's (decision 0.1): an MCP client is refused 428 and asks for nothing;
// without the acknowledgment nothing is asked for or removed; a key's first call asks for the
// owner's approval (409 approval_pending) and removes nothing while it is pending; once the
// owner approves it on the console, the key's retry naming it removes exactly the planned month
// files, with old's records (an intent before, an outcome after, connector audit, operation
// prune, target audit.log {before, removed}), on the server's own sink, and the chain still
// verifies. The owner signed in on the console prunes directly.
func TestAuditPruneIsTheOwners(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	g, sink, _ := pruneChain(t, dir)
	body := `{"before":"2026-11-01T00:00:00Z","acknowledged":true}`

	code, out := g.call(t, apiKey, "POST", "/api/audit/prune/plan", `{"before":"2026-11-01T00:00:00Z"}`)
	if code != http.StatusOK || !strings.Contains(out, `"files":["2026-09.jsonl","2026-10.jsonl"]`) {
		t.Fatalf("plan: %d %s", code, out)
	}
	code, out = g.call(t, localTrust, "POST", "/api/audit/prune", body)
	wantCode(t, "an MCP client pruning", code, out, http.StatusPreconditionRequired, "approval_required")
	code, out = g.call(t, apiKey, "POST", "/api/audit/prune", `{"before":"2026-11-01T00:00:00Z"}`)
	wantCode(t, "a prune without the acknowledgment", code, out, http.StatusConflict, "acknowledgment_required")
	code, out = g.call(t, ownerSession, "POST", "/api/audit/prune", `{"before":"2026-11-01T00:00:00Z"}`)
	wantCode(t, "the owner's prune without the acknowledgment", code, out, http.StatusConflict, "acknowledgment_required")
	if got := monthFiles(t, dir); len(got) != 3 {
		t.Fatalf("a refused prune removed files: %v", got)
	}

	id := g.approveAs(t, "/api/audit/prune", body)
	// The approval is for these files: another date is another request.
	code, out = g.call(t, apiKey, "POST", "/api/audit/prune", `{"before":"2026-10-01T00:00:00Z","acknowledged":true,"approval_id":"`+id+`"}`)
	if code == http.StatusOK {
		t.Fatalf("an approval for two files pruned another set: %s", out)
	}
	if got := monthFiles(t, dir); len(got) != 3 {
		t.Fatalf("a mismatched approval removed files: %v", got)
	}
	// A refused consume spends the approval; ask again.
	id = g.approveAs(t, "/api/audit/prune", body)
	code, out = g.call(t, otherKey, "POST", "/api/audit/prune", `{"before":"2026-11-01T00:00:00Z","acknowledged":true,"approval_id":"`+id+`"}`)
	if code == http.StatusOK {
		t.Fatalf("another key used the approval: %s", out)
	}
	id = g.approveAs(t, "/api/audit/prune", body)
	code, out = g.call(t, apiKey, "POST", "/api/audit/prune", `{"before":"2026-11-01T00:00:00Z","acknowledged":true,"approval_id":"`+id+`"}`)
	if code != http.StatusOK {
		t.Fatalf("the approved prune: %d %s", code, out)
	}
	var res gate.AuditPruneResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || strings.Join(res.Removed, ",") != "2026-09.jsonl,2026-10.jsonl" || res.Proof != "approval:"+id {
		t.Fatalf("the prune's answer: %s %v", out, err)
	}
	if got := monthFiles(t, dir); strings.Join(got, ",") != "2026-11.jsonl" {
		t.Fatalf("after the prune: %v", got)
	}
	checked, err := audit.Check(dir)
	if err != nil || checked.Err() != nil {
		t.Fatalf("the pruned chain does not verify: %v %v", err, checked.Err())
	}
	var intent, outcome *audit.Record
	for i, rec := range checked.Records {
		if rec.Connector != audit.PruneConnector || rec.Operation != audit.PruneOperation {
			continue
		}
		switch rec.Kind {
		case audit.KindIntent:
			intent = &checked.Records[i]
		case audit.KindOutcome:
			outcome = &checked.Records[i]
		}
	}
	if intent == nil || outcome == nil || intent.Target.Kind != audit.PruneTarget || outcome.Target.Fields["removed"] != "2026-09.jsonl,2026-10.jsonl" ||
		intent.Principal.Via != "api" || !intent.Acknowledged || outcome.OutcomeCode != audit.OutcomeOK {
		t.Fatalf("the prune's records: %+v %+v", intent, outcome)
	}

	// Nothing left before the date: old's "nothing to prune", 200 and nothing removed.
	code, out = g.call(t, ownerSession, "POST", "/api/audit/prune", body)
	if code != http.StatusOK || !strings.Contains(out, `"files":[]`) {
		t.Fatalf("nothing to prune: %d %s", code, out)
	}
	_ = sink
}

// The owner signed in on the console prunes directly, with the acknowledgment.
func TestTheOwnerOnTheConsolePrunes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	g, _, _ := pruneChain(t, dir)
	code, out := g.call(t, ownerSession, "POST", "/api/audit/prune", `{"before":"2026-10-01T00:00:00Z","acknowledged":true}`)
	if code != http.StatusOK || !strings.Contains(out, `"removed":["2026-09.jsonl"]`) || !strings.Contains(out, `"proof":"console"`) {
		t.Fatalf("the owner's prune: %d %s", code, out)
	}
}

// cutChain continues the old build's fixture chain and then cuts its last record at a line
// boundary, under the anchor: the problem only a person's reanchor acknowledges.
func cutChain(t *testing.T, dir string) *gatedAPI {
	t.Helper()
	head := copyOldChainInto(t, dir)
	if err := (auditchain.KeychainAnchor{Dir: dir}).Store(head); err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	g, sink := chainAPI(t, dir, clk)
	writeOps(t, sink, 2)
	// An anchored write, then the cut: the file loses its last line.
	if _, err := sink.Write(audit.Record{Kind: audit.KindApprovalDecided, Connector: "approvals", Operation: "decide",
		Principal: audit.Principal{Kind: "human", Surface: "web", Via: "web"}}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, head.File)
	data, err := os.ReadFile(file) //nolint:gosec // the test's own dir
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(strings.TrimSuffix(string(data), "\n"), "\n")
	if err = os.WriteFile(file, []byte(strings.Join(lines[:len(lines)-1], "")), 0o600); err != nil { //nolint:gosec // as above
		t.Fatal(err)
	}
	return g
}

// copyOldChainInto copies the old build's chain fixture (internal/auditchain/testdata/oldchain)
// into dir and returns the head the old build anchored.
func copyOldChainInto(t *testing.T, dir string) audit.Head {
	t.Helper()
	src := filepath.Join("..", "auditchain", "testdata", "oldchain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var head audit.Head
	raw, err := os.ReadFile(filepath.Join(src, "anchor.json")) //nolint:gosec // a test fixture
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &head); err != nil {
		t.Fatal(err)
	}
	chain, err := os.ReadFile(filepath.Join(src, head.File)) //nolint:gosec // as above
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, head.File), chain, 0o600); err != nil { //nolint:gosec // the test's own dir
		t.Fatal(err)
	}
	return head
}

// audit reanchor is the owner's too: an MCP client is refused 428; a key asks for the owner's
// approval and writes nothing until it names one the owner approved; the record is old's
// chain_reanchored, which moves the anchor, after which the chain verifies again; a chain that
// vouches for its tail has nothing to reanchor (200, as old answered).
func TestAuditReanchorIsTheOwners(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	g := cutChain(t, dir)
	code, out := g.call(t, apiKey, "POST", "/api/audit/reanchor/plan", "")
	if code != http.StatusOK || !strings.Contains(out, `"tail_trusted":false`) || !strings.Contains(out, "the log was cut") {
		t.Fatalf("plan: %d %s", code, out)
	}
	before := out
	code, out = g.call(t, localTrust, "POST", "/api/audit/reanchor", `{"acknowledged":true}`)
	wantCode(t, "an MCP client reanchoring", code, out, http.StatusPreconditionRequired, "approval_required")
	code, out = g.call(t, apiKey, "POST", "/api/audit/reanchor", `{}`)
	wantCode(t, "a reanchor without the acknowledgment", code, out, http.StatusConflict, "acknowledgment_required")

	id := g.approveAs(t, "/api/audit/reanchor", `{"acknowledged":true}`)
	// The approval's own records reword the cut; it is the same cut, so the approval still holds.
	if _, after := g.call(t, apiKey, "POST", "/api/audit/reanchor/plan", ""); !strings.Contains(before, "past the end") || !strings.Contains(after, "not where it was written") {
		t.Fatalf("the cut was not reworded by the approval's records:\nbefore %s\nafter %s", before, after)
	}
	code, out = g.call(t, apiKey, "POST", "/api/audit/reanchor", `{"acknowledged":true,"approval_id":"`+id+`"}`)
	if code != http.StatusOK || !strings.Contains(out, `"seq":`) || !strings.Contains(out, `"proof":"approval:`+id+`"`) {
		t.Fatalf("the approved reanchor: %d %s", code, out)
	}
	checked, err := audit.Check(dir)
	if err != nil || checked.Err() != nil || !checked.TailTrusted() {
		t.Fatalf("after the reanchor: %v %v", err, checked.Err())
	}
	last := checked.Records[len(checked.Records)-1]
	if last.Kind != audit.KindChainReanchored || last.Principal.Via != "api" || !strings.Contains(last.Note, "the log was cut") {
		t.Fatalf("the reanchor's record: %+v", last)
	}
	code, out = g.call(t, ownerSession, "POST", "/api/audit/reanchor", `{"acknowledged":true}`)
	if code != http.StatusOK || !strings.Contains(out, `"tail_trusted":true`) || strings.Contains(out, `"seq"`) {
		t.Fatalf("nothing to reanchor: %d %s", code, out)
	}
}

// Observe mode keeps no chain, and a gate whose sink has no directory has none to maintain.
func TestTheAuditLaneNeedsTheServersChain(t *testing.T) {
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	g := &gatedAPI{h: a.Mux()}
	code, out := g.call(t, ownerSession, "POST", "/api/audit/reanchor", `{"acknowledged":true}`)
	wantCode(t, "observe mode", code, out, http.StatusServiceUnavailable, "audit_unavailable")
	mem := newGatedAPI(t, gatetest.Options{}, nil, nil)
	code, out = mem.call(t, ownerSession, "POST", "/api/audit/prune", `{"before":"2026-11-01T00:00:00Z","acknowledged":true}`)
	wantCode(t, "a memory sink", code, out, http.StatusServiceUnavailable, "audit_unavailable")
}

// laneOut is where TestAuditLaneChainsForTheOldVerify keeps a scenario's chain and anchor, for
// the old binary's audit verify (scripts/verify-audit-lane.sh), or a temp dir.
func laneOut(t *testing.T, scenario string) string {
	t.Helper()
	if out := os.Getenv("CERBERUS_AUDIT_LANE_OUT"); out != "" {
		return filepath.Join(out, scenario)
	}
	return filepath.Join(t.TempDir(), scenario)
}

// keepAnchor writes the chain's keychain anchor beside it as anchor.json, for the script to put
// in its scratch keyring.
func keepAnchor(t *testing.T, dir string) {
	t.Helper()
	head, ok, err := (auditchain.KeychainAnchor{Dir: dir}).Load()
	if err != nil || !ok {
		t.Fatalf("no anchor for %s: %v", dir, err)
	}
	raw, _ := json.Marshal(head)
	if err = os.WriteFile(filepath.Join(filepath.Dir(dir), "anchor.json"), raw, 0o600); err != nil { //nolint:gosec // the scenario's own dir
		t.Fatal(err)
	}
}

// The chains the audit lane leaves, for the old build's `audit verify`: a prune (verifies), a
// cut the owner reanchored (verifies: the reanchor moved the anchor), and a rewritten record the
// owner reanchored (old reports it, "acknowledged by 1 reanchor(s)", as it always did: a
// reanchor rewrites nothing). Each is checked here with this build's audit.Check, and kept under
// CERBERUS_AUDIT_LANE_OUT/<scenario>/{audit,anchor.json} when that is set.
func TestAuditLaneChainsForTheOldVerify(t *testing.T) {
	// A prune: September and October removed through the lane, then November continues.
	pruneDir := filepath.Join(laneOut(t, "prune"), "audit")
	g, sink, clk := pruneChain(t, pruneDir)
	if code, out := g.call(t, ownerSession, "POST", "/api/audit/prune", `{"before":"2026-11-01T00:00:00Z","acknowledged":true}`); code != http.StatusOK {
		t.Fatalf("prune: %d %s", code, out)
	}
	clk.set(time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC))
	writeOps(t, sink, 1)
	if checked, err := audit.Check(pruneDir); err != nil || checked.Err() != nil {
		t.Fatalf("pruned chain: %v %v", err, checked.Err())
	}
	keepAnchor(t, pruneDir)

	// A cut, reanchored by the owner through a key and the console approval.
	cutDir := filepath.Join(laneOut(t, "reanchor-cut"), "audit")
	g = cutChain(t, cutDir)
	id := g.approveAs(t, "/api/audit/reanchor", `{"acknowledged":true}`)
	if code, out := g.call(t, apiKey, "POST", "/api/audit/reanchor", `{"acknowledged":true,"approval_id":"`+id+`"}`); code != http.StatusOK {
		t.Fatalf("reanchor the cut: %d %s", code, out)
	}
	if checked, err := audit.Check(cutDir); err != nil || checked.Err() != nil {
		t.Fatalf("reanchored cut: %v %v", err, checked.Err())
	}
	keepAnchor(t, cutDir)

	// A rewritten record, reanchored by the owner on the console, then more records.
	breakDir := filepath.Join(laneOut(t, "reanchor-break"), "audit")
	head := copyOldChainInto(t, breakDir)
	if err := (auditchain.KeychainAnchor{Dir: breakDir}).Store(head); err != nil {
		t.Fatal(err)
	}
	clk = &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	g, sink = chainAPI(t, breakDir, clk)
	writeOps(t, sink, 3)
	file := filepath.Join(breakDir, head.File)
	data, err := os.ReadFile(file) //nolint:gosec // the test's own dir
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	lines[len(lines)-3] = strings.Replace(lines[len(lines)-3], `"operation":"stop"`, `"operation":"stap"`, 1)
	if err = os.WriteFile(file, []byte(strings.Join(lines, "")), 0o600); err != nil { //nolint:gosec // as above
		t.Fatal(err)
	}
	if code, out := g.call(t, ownerSession, "POST", "/api/audit/reanchor", `{"acknowledged":true}`); code != http.StatusOK || !strings.Contains(out, "hash does not match") {
		t.Fatalf("reanchor the break: %d %s", code, out)
	}
	writeOps(t, sink, 1)
	checked, err := audit.Check(breakDir)
	if err != nil || checked.Err() == nil || !checked.TailTrusted() {
		t.Fatalf("a reanchored break still reports its problem and trusts what follows: %v %v", err, checked.Err())
	}
	keepAnchor(t, breakDir)
}

// breakChain continues the old fixture chain with three operations, and returns the file and
// a function that rewrites the operation on line n from the end under its hash (or restores it).
func breakChain(t *testing.T, dir string) (*gatedAPI, func(n int, tamper bool)) {
	t.Helper()
	head := copyOldChainInto(t, dir)
	if err := (auditchain.KeychainAnchor{Dir: dir}).Store(head); err != nil {
		t.Fatal(err)
	}
	g, sink := chainAPI(t, dir, &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)})
	writeOps(t, sink, 3)
	file := filepath.Join(dir, head.File)
	data, err := os.ReadFile(file) //nolint:gosec // the test's own dir
	if err != nil {
		t.Fatal(err)
	}
	pristine := strings.SplitAfter(string(data), "\n")
	return g, func(n int, tamper bool) {
		now, rerr := os.ReadFile(file) //nolint:gosec // as above
		if rerr != nil {
			t.Fatal(rerr)
		}
		lines := strings.SplitAfter(string(now), "\n")
		i := len(pristine) - 1 - n
		lines[i] = pristine[i]
		if tamper {
			lines[i] = strings.Replace(pristine[i], `"operation":"stop"`, `"operation":"stap"`, 1)
		}
		if werr := os.WriteFile(file, []byte(strings.Join(lines, "")), 0o600); werr != nil { //nolint:gosec // as above
			t.Fatal(werr)
		}
	}
}

// A reanchor's approval is for the tampering it was asked about: an approval for a rewritten
// record A does not acknowledge a different rewritten record B, though the chain shows as many
// problems (409 plan_stale, nothing reanchored); an approval for A, still A after the approval's
// own records, reanchors it.
func TestAReanchorApprovalIsForItsTampering(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	g, rewrite := breakChain(t, dir)
	rewrite(2, true) // A
	id := g.approveAs(t, "/api/audit/reanchor", `{"acknowledged":true}`)
	rewrite(2, false)
	rewrite(4, true) // B: another record, one problem as before
	_, plan := g.call(t, apiKey, "POST", "/api/audit/reanchor/plan", "")
	if strings.Count(plan, "hash does not match") != 1 {
		t.Fatalf("B is not one problem: %s", plan)
	}
	code, out := g.call(t, apiKey, "POST", "/api/audit/reanchor", `{"acknowledged":true,"approval_id":"`+id+`"}`)
	wantCode(t, "an approval for A used on B", code, out, http.StatusConflict, "plan_stale")
	if checked, err := audit.Check(dir); err != nil || checked.TailTrusted() {
		t.Fatalf("B was reanchored: %v", err)
	}

	rewrite(4, false)
	rewrite(2, true) // A again
	id = g.approveAs(t, "/api/audit/reanchor", `{"acknowledged":true}`)
	code, out = g.call(t, apiKey, "POST", "/api/audit/reanchor", `{"acknowledged":true,"approval_id":"`+id+`"}`)
	if code != http.StatusOK || !strings.Contains(out, `"seq":`) {
		t.Fatalf("the approved reanchor of A: %d %s", code, out)
	}
	if checked, err := audit.Check(dir); err != nil || !checked.TailTrusted() {
		t.Fatalf("A was not reanchored: %v", err)
	}
}
