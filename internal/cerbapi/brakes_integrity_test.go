package cerbapi

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// fileBrakes installs brakes read against a real audit directory, as the
// daemon's are.
func fileBrakes(t *testing.T) (brake.Store, *audit.FileSink) {
	t.Helper()
	auditDir := filepath.Join(t.TempDir(), "audit")
	sink, err := audit.OpenFileSink(auditDir)
	if err != nil {
		t.Fatal(err)
	}
	store := brake.Store{Dir: t.TempDir()}
	SetBrakes(&Brakes{Store: store, AuditDir: auditDir, Sink: sink})
	t.Cleanup(func() { SetBrakes(nil) })
	return store, sink
}

// Deleting the store does not lift a lockdown the log recorded (M1): the
// brakes restore it, and a person lifts it as usual. While the log's chain
// does not vouch for its tail, a lift is refused, since its record would
// not count; a reanchor lets it through.
func TestALockdownTheLogHoldsIsRestoredAndLiftedOnlyOnATrustedChain(t *testing.T) {
	store, sink := fileBrakes(t)
	noPasskeys(t, sink)
	person := callerAs(confirmHuman, SurfaceSocket)
	if _, _, err := EngageLockdown(person, sink, store, "incident"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.Dir, brake.FileName)); err != nil {
		t.Fatal(err)
	}
	b := ProcessBrakes()
	if b.Current().Lockdown == nil {
		t.Fatal("deleting the store lifted the lockdown")
	}
	if st, _ := store.Load(); st.Lockdown == nil {
		t.Fatal("the lockdown was not restored to the store")
	}
	// Break the audit chain: the lift is refused, naming the recovery.
	file, _ := filepath.Glob(filepath.Join(sink.Dir(), "*.jsonl"))
	data, _ := os.ReadFile(file[0])
	if err := os.WriteFile(file[0], bytes.Replace(data, []byte("incident"), []byte("accident"), 1), 0o600); err != nil { //nolint:gosec // the test's own temp file
		t.Fatal(err)
	}
	_, err := LiftLockdown(person, sink, store, "")
	if connectorErrorCode(err) != ExternalConnectorAuditUnavailable || !strings.Contains(err.Error(), "cerberus audit reanchor") {
		t.Fatalf("a lift past an audit break: %v", err)
	}
	if st, _ := store.Load(); st.Lockdown == nil {
		t.Fatal("a refused lift lifted")
	}
	if _, rerr := audit.Reanchor(sink, sink.Dir(), audit.Principal{Kind: "human"}); rerr != nil {
		t.Fatal(rerr)
	}
	st, err := LiftLockdown(person, sink, store, "")
	if err != nil || st.Lockdown != nil || b.Current().Lockdown != nil {
		t.Fatalf("a lift after the reanchor: %v %+v", err, st)
	}
}

// The refusal's recovery instruction survives redaction.
func TestTheLiftRefusalSurvivesRedaction(t *testing.T) {
	msg := "the audit log's chain does not verify (1 problem(s)), and a lift recorded past the break is not applied; nothing was lifted. Check it with `cerberus audit verify`, then a person runs `cerberus audit reanchor` and lifts again"
	if got := redact.Text(msg); got != msg {
		t.Fatalf("redacted to %q", got)
	}
}

// A caller's claimed name is clipped on a character boundary, and a record
// carrying it verifies (M2).
func TestClipKeepsValidUTF8(t *testing.T) {
	name := strings.Repeat("é", 100) // 200 bytes
	got := clip(name)
	if !utf8.ValidString(got) || len(got) > 128 || len(got) < 126 {
		t.Fatalf("clip gave %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	if bad := clip("ok\xff"); !utf8.ValidString(bad) {
		t.Fatalf("an invalid claim was kept invalid: %q", bad)
	}
	dir := t.TempDir()
	sink, err := audit.OpenFileSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write(audit.Record{Kind: audit.KindIntent, Principal: audit.Principal{Surface: "socket", Client: got}}); err != nil {
		t.Fatal(err)
	}
	if err := audit.Verify(dir); err != nil {
		t.Fatal(err)
	}
}
