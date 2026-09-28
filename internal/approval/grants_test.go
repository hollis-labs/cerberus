package approval

import (
	"errors"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
)

var devBox = audit.Target{Kind: "local.resource", Resource: "dev-box", Fields: map[string]string{"id": "dev-box"}, Env: "dev"}

func grant(t *testing.T, s *Store, scope string, p audit.Principal, ttl time.Duration) Approval {
	t.Helper()
	a, err := s.Request(Approval{Principal: p, Connector: "local", Operation: "reload", Effect: "lifecycle", Target: devBox,
		ArgsDigest: "hmac:args", PlanHash: "sha256:plan", Channel: ChannelTTYConfirm, Scope: scope}, ttl)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(a.ID, Decision{Approve: true, By: human}, nil); err != nil {
		t.Fatal(err)
	}
	return a
}

func use(p audit.Principal, target audit.Target) GrantCheck {
	return GrantCheck{Connector: "local", Operation: "reload", Target: target, Principal: p, OperationID: "op"}
}

// A window grant covers every call of its operation on its target by its
// requester, and counts each use without being spent by it; it is valid
// for its TTL from when it was approved.
func TestWindowGrantIsReusedUntilItExpires(t *testing.T) {
	s, c, dir := newStore(t)
	g := grant(t, s, ScopeWindow, asker, 30*time.Minute)
	c.t = c.t.Add(10 * time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := s.UseGrant(use(asker, devBox), nil); err != nil {
			t.Fatalf("use %d: %v", i, err)
		}
	}
	got, _ := s.Get(g.ID)
	if got.Status != Approved || got.Uses != 3 || !got.ExpiresAt.Equal(time.Date(2026, 9, 26, 12, 30, 0, 0, time.UTC)) {
		t.Fatalf("grant %+v", got)
	}
	reopened, err := open(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := reopened.Get(g.ID); again.Uses != 3 {
		t.Fatalf("a restart lost the uses: %+v", again)
	}
	c.t = c.t.Add(25 * time.Minute)
	if _, err := s.UseGrant(use(asker, devBox), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired grant was used: %v", err)
	}
}

// A grant covers only its target, and only its requester: an agent's grant
// never covers a human's call, or the reverse.
func TestGrantCoversItsTargetAndRequester(t *testing.T) {
	s, _, _ := newStore(t)
	grant(t, s, ScopeWindow, asker, time.Hour)
	other := devBox
	other.Resource, other.Fields = "prod-box", map[string]string{"id": "prod-box"}
	for name, c := range map[string]GrantCheck{
		"another target":    use(asker, other),
		"another kind":      use(audit.Principal{Kind: "human", Via: asker.Via}, devBox),
		"another channel":   use(audit.Principal{Kind: asker.Kind, Via: "mcp_http"}, devBox),
		"another operation": {Connector: "local", Operation: "stop", Target: devBox, Principal: asker},
	} {
		if _, err := s.UseGrant(c, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.UseGrant(use(audit.Principal{Kind: asker.Kind, Via: asker.Via, Session: "another"}, devBox), nil); err != nil {
		t.Fatalf("a window grant is not bound to a session: %v", err)
	}
}

// A session grant is its session's; with no session it is a once approval.
func TestSessionGrant(t *testing.T) {
	s, _, _ := newStore(t)
	mine := audit.Principal{Kind: "agent", Via: "mcp_stdio", Session: "s1"}
	grant(t, s, ScopeSession, mine, time.Hour)
	if _, err := s.UseGrant(use(mine, devBox), nil); err != nil {
		t.Fatalf("its session: %v", err)
	}
	theirs := mine
	theirs.Session = "s2"
	if _, err := s.UseGrant(use(theirs, devBox), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another session used it: %v", err)
	}
	noSession := grant(t, s, ScopeSession, audit.Principal{Kind: "agent", Via: "mcp_stdio"}, time.Hour)
	if got, _ := s.Get(noSession.ID); got.Scope != ScopeOnce || got.IsGrant() {
		t.Fatalf("a session grant without a session: %+v", got)
	}
}

// A revoked grant is not used; a policy that now denies refuses its use;
// an out-of-band grant's presence is verified on every use.
func TestGrantUseIsRecheckedEachTime(t *testing.T) {
	s, _, _ := newStore(t)
	g := grant(t, s, ScopeWindow, asker, time.Hour)
	deny := use(asker, devBox)
	deny.ReauthorizedDeny = true
	if _, err := s.UseGrant(deny, nil); !errors.Is(err, ErrPolicyNowDenys) {
		t.Fatalf("a deny: %v", err)
	}
	if _, err := s.Revoke(g.ID, Decision{By: human}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseGrant(use(asker, devBox), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked grant: %v", err)
	}

	oob, err := s.Request(Approval{Principal: asker, Connector: "local", Operation: "reload", Target: devBox, Channel: ChannelOutOfBand, Scope: ScopeWindow}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(oob.ID, Decision{Approve: true, By: human, KeyFingerprint: "k", Assertion: []byte("signed")}, fakePresence{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseGrant(use(asker, devBox), NoPresence{}); !errors.Is(err, ErrNoPresence) {
		t.Fatalf("an out-of-band grant used without verifying: %v", err)
	}
	if _, err := s.UseGrant(use(asker, devBox), fakePresence{}); err != nil {
		t.Fatalf("verified: %v", err)
	}
}

// A retry naming a grant by id uses it rather than spending it.
func TestConsumeByIDUsesAGrant(t *testing.T) {
	s, _, _ := newStore(t)
	g := grant(t, s, ScopeWindow, asker, time.Hour)
	check := ConsumeCheck{Connector: "local", Operation: "reload", Principal: asker, Target: devBox, ArgsDigest: "hmac:other", OperationID: "op1"}
	for i := 0; i < 2; i++ {
		if _, err := s.Consume(g.ID, check, nil); err != nil {
			t.Fatalf("use %d: %v", i, err)
		}
	}
	if got, _ := s.Get(g.ID); got.Status != Approved || got.Uses != 2 {
		t.Fatalf("grant %+v", got)
	}
}
