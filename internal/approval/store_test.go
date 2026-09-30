package approval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// fakePresence verifies a decision whose assertion is "signed".
type fakePresence struct{}

func (fakePresence) Verify(_ Approval, d Decision) error {
	if string(d.Assertion) != "signed" {
		return ErrNoPresence
	}
	return nil
}

func newStore(t *testing.T) (*Store, *clock, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "approvals")
	c := &clock{t: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	s, err := open(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	return s, c, dir
}

func request(t *testing.T, s *Store, channel string) Approval {
	t.Helper()
	a, err := s.Request(Approval{Principal: asker, Connector: "kubernetes", Operation: "delete_pod",
		Effect: "destructive", Target: audit.Target{Kind: "kubernetes.pod", Env: "prod"}, ArgsDigest: "hmac:args", PlanHash: "sha256:plan",
		Rule: "builtin.env-prod", Channel: channel}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var human = audit.Principal{Kind: "human", Via: "web"}

func TestLifecycle(t *testing.T) {
	s, c, dir := newStore(t)
	a := request(t, s, ChannelOutOfBand)
	if a.Status != Pending || a.Scope != ScopeOnce || !strings.HasPrefix(a.ID, "apr_") || a.ApproveWith() != "cerberus approvals approve "+a.ID {
		t.Fatalf("requested %+v", a)
	}
	// Out of band without a presence proof is not approved.
	if _, derr := s.Decide(a.ID, Decision{Approve: true, By: human}, NoPresence{}); !errors.Is(derr, ErrNoPresence) {
		t.Fatalf("decide without presence: %v", derr)
	}
	a, err := s.Decide(a.ID, Decision{Approve: true, By: human, Assertion: []byte("signed")}, fakePresence{})
	if err != nil || a.Status != Approved {
		t.Fatalf("decide: %+v %v", a, err)
	}
	if _, err = s.Decide(a.ID, Decision{Approve: true, By: human}, fakePresence{}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("decided twice: %v", err)
	}
	check := ConsumeCheck{Principal: asker, Connector: "kubernetes", Operation: "delete_pod", ArgsDigest: "hmac:args", PlanHash: "sha256:plan", OperationID: "op1"}
	for name, tc := range map[string]struct {
		check ConsumeCheck
		want  error
	}{
		"swapped args":   {ConsumeCheck{Principal: asker, Connector: "kubernetes", Operation: "delete_pod", ArgsDigest: "hmac:other", PlanHash: "sha256:plan"}, ErrArgsMismatch},
		"another plan":   {ConsumeCheck{Principal: asker, Connector: "kubernetes", Operation: "delete_pod", ArgsDigest: "hmac:args", PlanHash: "sha256:new"}, ErrPlanStale},
		"policy denies":  {ConsumeCheck{Principal: asker, Connector: "kubernetes", Operation: "delete_pod", ArgsDigest: "hmac:args", PlanHash: "sha256:plan", ReauthorizedDeny: true}, ErrPolicyNowDenys},
		"another caller": {ConsumeCheck{Principal: audit.Principal{Kind: "agent", Via: "mcp_http"}, Connector: "kubernetes", Operation: "delete_pod", ArgsDigest: "hmac:args", PlanHash: "sha256:plan"}, ErrOtherPrincipal},
		"another op":     {ConsumeCheck{Principal: asker, Connector: "kubernetes", Operation: "scale_workload", ArgsDigest: "hmac:args", PlanHash: "sha256:plan"}, ErrOtherOperation},
	} {
		if _, cerr := s.Consume(a.ID, tc.check, fakePresence{}); !errors.Is(cerr, tc.want) {
			t.Errorf("%s: %v, want %v", name, cerr, tc.want)
		}
	}
	a, err = s.Consume(a.ID, check, fakePresence{})
	if err != nil || a.Status != Consumed || a.ConsumedOperationID != "op1" {
		t.Fatalf("consume: %+v %v", a, err)
	}
	if _, err = s.Consume(a.ID, check, fakePresence{}); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("consumed twice: %v", err)
	}

	// A restart folds the same state back. It reopens on the test's clock:
	// Open's is the wall clock, which passes the fixture's expiry.
	again, err := open(dir, c.now)
	if err != nil || len(again.Problems) != 0 {
		t.Fatalf("reopen: %v %v", err, again.Problems)
	}
	if got, ok := again.Get(a.ID); !ok || got.Status != Consumed || got.Decision == nil || !got.Decision.Approve {
		t.Fatalf("folded %+v", got)
	}
}

func TestDenyRevokeAndExpiry(t *testing.T) {
	s, c, _ := newStore(t)
	denied := request(t, s, ChannelTTYConfirm)
	if a, err := s.Decide(denied.ID, Decision{Approve: false, By: human, Reason: "not now"}, nil); err != nil || a.Status != Denied {
		t.Fatalf("deny: %+v %v", a, err)
	}
	revoked := request(t, s, ChannelTTYConfirm)
	if _, err := s.Revoke(revoked.ID, Decision{By: human}); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("revoked a pending approval: %v", err)
	}
	if _, err := s.Decide(revoked.ID, Decision{Approve: true, By: human}, nil); err != nil {
		t.Fatal(err)
	}
	if a, err := s.Revoke(revoked.ID, Decision{By: human}); err != nil || a.Status != Revoked || a.RevokedBy == nil {
		t.Fatalf("revoke: %+v %v", a, err)
	}
	stale := request(t, s, ChannelTTYConfirm)
	c.t = c.t.Add(2 * time.Hour)
	// Lazily expired on read, before the sweeper runs.
	if a, _ := s.Get(stale.ID); a.Status != Expired {
		t.Fatalf("lazy expiry: %s", a.Status)
	}
	if _, err := s.Decide(stale.ID, Decision{Approve: true, By: human}, nil); !errors.Is(err, ErrExpired) {
		t.Fatalf("decided an expired approval: %v", err)
	}
	swept, err := s.Sweep()
	if err != nil || len(swept) != 1 || swept[0].ID != stale.ID {
		t.Fatalf("sweep: %+v %v", swept, err)
	}
	if again, _ := s.Sweep(); len(again) != 0 {
		t.Fatal("swept twice")
	}
	if n := len(s.List()); n != 3 {
		t.Fatalf("list %d", n)
	}
}

// The store is not trusted on its word: a forged "approved" line — the
// chain rebuilt by whoever wrote it — is folded, but an out-of-band
// approval without a verifiable presence proof is never consumed.
func TestForgedApprovalDoesNotConsume(t *testing.T) {
	s, c, dir := newStore(t)
	a := request(t, s, ChannelOutOfBand)
	path := filepath.Join(dir, FileName)
	forged := Event{V: eventVersion, Seq: s.seq + 1, Time: c.now().UTC(), Type: EventDecided, ApprovalID: a.ID,
		Decision: &Decision{Approve: true, By: human, Assertion: []byte("forged")}, PrevHash: s.last}
	forged.Hash = hashEvent(forged)
	line, _ := json.Marshal(forged)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the test's own store
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()

	reopened, err := open(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reopened.Get(a.ID); got.Status != Approved {
		t.Fatalf("the forged line did not fold: %s", got.Status)
	}
	check := ConsumeCheck{Principal: asker, Connector: "kubernetes", Operation: "delete_pod", ArgsDigest: "hmac:args", PlanHash: "sha256:plan"}
	for name, v := range map[string]PresenceVerifier{"no verifier": NoPresence{}, "a real verifier": fakePresence{}} {
		if _, err := reopened.Consume(a.ID, check, v); !errors.Is(err, ErrNoPresence) {
			t.Errorf("%s consumed a forged approval: %v", name, err)
		}
	}
}

func TestDamagedStoreFoldsAndReports(t *testing.T) {
	s, c, dir := newStore(t)
	request(t, s, ChannelTTYConfirm)
	path := filepath.Join(dir, FileName)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the test's own store
	_, _ = f.WriteString("{not json\n")
	_, _ = f.WriteString(`{"v":1,"seq":9,"type":"consumed","approval_id":"apr_ghost","prev_hash":"x","hash":"y"}` + "\n")
	_ = f.Close()
	reopened, err := open(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Problems) < 2 || len(reopened.List()) != 1 {
		t.Fatalf("problems %v, list %d", reopened.Problems, len(reopened.List()))
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
}

// asker is the principal the tests' approvals are asked for by.
var asker = audit.Principal{Kind: "agent", Via: "mcp_stdio"}

// appendRaw writes ev as its own line, chained as given (tests forge).
func appendRaw(t *testing.T, dir string, ev Event) {
	t.Helper()
	line, _ := json.Marshal(ev)
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the test's own store
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

// Past a broken chain only what restricts applies (H4): an approval
// written there does not fold, a consume still does. The daemon reanchors
// on open, and events after that are trusted again.
func TestNothingPermissiveFoldsPastABreak(t *testing.T) {
	s, c, dir := newStore(t)
	pendingOne := request(t, s, ChannelTTYConfirm)
	approvedOne := request(t, s, ChannelTTYConfirm)
	if _, err := s.Decide(approvedOne.ID, Decision{Approve: true, By: human}, nil); err != nil {
		t.Fatal(err)
	}
	// A break: a line that does not chain.
	appendRaw(t, dir, Event{V: eventVersion, Seq: 99, Type: EventDecided, ApprovalID: pendingOne.ID, Decision: &Decision{Approve: true, By: human}, PrevHash: "x", Hash: "y"})
	// After it, a correctly shaped approve and a consume.
	appendRaw(t, dir, Event{V: eventVersion, Seq: 100, Type: EventConsumed, ApprovalID: approvedOne.ID, PrevHash: "y", Hash: "z"})
	reopened, err := open(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reopened.Get(pendingOne.ID); got.Status != Pending {
		t.Fatalf("an approval past the break folded: %s", got.Status)
	}
	if got, _ := reopened.Get(approvedOne.ID); got.Status != Consumed {
		t.Fatalf("a consume past the break did not fold: %s", got.Status)
	}
	// Reanchored: a decision made now survives the next open.
	if _, err = reopened.Decide(pendingOne.ID, Decision{Approve: true, By: human}, nil); err != nil {
		t.Fatal(err)
	}
	again, err := open(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := again.Get(pendingOne.ID); got.Status != Approved {
		t.Fatalf("a decision after the reanchor did not fold: %s", got.Status)
	}
}

// A call that needs out of band now refuses an approval the store says was
// met on a terminal (H4).
func TestConsumeRefusesAWeakerChannel(t *testing.T) {
	s, _, _ := newStore(t)
	a := request(t, s, ChannelTTYConfirm)
	if _, err := s.Decide(a.ID, Decision{Approve: true, By: human}, nil); err != nil {
		t.Fatal(err)
	}
	check := ConsumeCheck{Principal: asker, Connector: "kubernetes", Operation: "delete_pod", ArgsDigest: "hmac:args", PlanHash: "sha256:plan", RequireOutOfBand: true}
	if _, err := s.Consume(a.ID, check, nil); !errors.Is(err, ErrWeakerChannel) {
		t.Fatalf("a terminal approval met a passkey requirement: %v", err)
	}
	check.RequireOutOfBand = false
	if _, err := s.Consume(a.ID, check, nil); err != nil {
		t.Fatalf("the same approval where a terminal suffices: %v", err)
	}
}
