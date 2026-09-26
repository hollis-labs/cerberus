package presence

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/presence/presencetest"
)

const origin = "http://localhost:4783"

type authenticator = presencetest.Authenticator

func newAuthenticator(t *testing.T) *authenticator {
	t.Helper()
	return presencetest.New(t, RPID, origin)
}

// allow is `cerberus approvals enroll`: a token, whose digest svc accepts.
func allow(t *testing.T, svc *Service) string {
	t.Helper()
	token, digest := NewEnrollToken()
	if err := svc.AllowEnrollment(digest); err != nil {
		t.Fatal(err)
	}
	return token
}

type fixture struct {
	svc      *Service
	dir      string
	sink     *audit.Memory
	notified []string
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{dir: t.TempDir(), sink: audit.NewMemory(), now: time.Now()}
	f.svc = New(f.dir, f.sink, Options{Origins: func() []string { return []string{origin} },
		Notify: func(title, _ string) { f.notified = append(f.notified, title) }, Now: func() time.Time { return f.now }})
	return f
}

var human = audit.Principal{Kind: "human", Via: "web", Session: "s1"}

func (f *fixture) enroll(t *testing.T, a *authenticator, authorizer *authenticator) error {
	t.Helper()
	begin, err := f.svc.BeginEnroll(allow(t, f.svc), origin, "test key")
	if err != nil {
		return err
	}
	var authorize json.RawMessage
	if authorizer != nil && begin.Authorize != nil {
		authorize = authorizer.Assert(t, begin.Authorize)
	}
	_, err = f.svc.FinishEnroll(human, begin.Ceremony, a.Register(t, begin.Creation), authorize)
	return err
}

func pending() approval.Approval {
	return approval.Approval{ID: "apr_1", Status: approval.Pending, Connector: "docker", Operation: "stop", ArgsDigest: "d", PlanHash: "sha256:p",
		Principal: audit.Principal{Kind: "agent", Via: "mcp_stdio"}, Channel: approval.ChannelOutOfBand}
}

// approve runs the approval ceremony with a and returns the decision the
// broker would store.
func (f *fixture) approve(t *testing.T, a *authenticator, ap approval.Approval) (approval.Decision, error) {
	t.Helper()
	cer, opts, err := f.svc.BeginApproval(ap, origin)
	if err != nil {
		return approval.Decision{}, err
	}
	raw, _ := json.Marshal(map[string]any{"ceremony": cer, "credential": a.Assert(t, opts)})
	sealedAssertion, _, err := f.svc.Seal(ap.ID, raw)
	if err != nil {
		return approval.Decision{}, err
	}
	d := approval.Decision{Approve: true, Assertion: sealedAssertion}
	return d, f.svc.Verify(ap, d)
}

// An enrolled passkey approves; the same assertion verifies again at
// consume; and the first enrollment is recorded, with a notification.
func TestEnrolledPasskeyApproves(t *testing.T) {
	f := newFixture(t)
	if st := f.svc.Status(); st.State != StateNotSetUp {
		t.Fatalf("state before enrollment = %s", st.State)
	}
	if _, _, err := f.svc.BeginApproval(pending(), origin); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("approval with no key: %v", err)
	}
	key := newAuthenticator(t)
	if err := f.enroll(t, key, nil); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if st := f.svc.Status(); st.State != StateOK || len(st.Keys) != 1 || st.LastEnrolledAt.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	if len(f.notified) != 1 || !strings.Contains(f.notified[0], "enrolled") {
		t.Fatalf("notifications = %v", f.notified)
	}
	d, err := f.approve(t, key, pending())
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	consumed := pending()
	consumed.Status = approval.Approved
	if err := f.svc.Verify(consumed, d); err != nil {
		t.Fatalf("consume-time re-verify: %v", err)
	}
	found := false
	for _, rec := range f.sink.Records() {
		found = found || (rec.Kind == audit.KindEnrollmentChanged && rec.Target.Fields["change"] == "enrolled")
	}
	if !found {
		t.Fatal("no enrollment_changed record")
	}
}

// A passkey that is not enrolled cannot approve, one without user
// verification cannot approve, and one used from another origin cannot.
func TestOnlyAnEnrolledVerifiedPasskeyApproves(t *testing.T) {
	f := newFixture(t)
	if err := f.enroll(t, newAuthenticator(t), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.approve(t, newAuthenticator(t), pending()); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("a passkey that is not enrolled: %v", err)
	}

	g := newFixture(t)
	noUV := newAuthenticator(t)
	if err := g.enroll(t, noUV, nil); err != nil {
		t.Fatal(err)
	}
	noUV.UV = false
	if _, err := g.approve(t, noUV, pending()); !errors.Is(err, ErrAssertion) {
		t.Fatalf("no user verification: %v", err)
	}
	elsewhere := newAuthenticator(t)
	h := newFixture(t)
	if err := h.enroll(t, elsewhere, nil); err != nil {
		t.Fatal(err)
	}
	elsewhere.Origin = "http://localhost:3000"
	if _, err := h.approve(t, elsewhere, pending()); !errors.Is(err, ErrAssertion) {
		t.Fatalf("an assertion from another page: %v", err)
	}
	if _, _, err := h.svc.BeginApproval(pending(), "http://localhost:3000"); !errors.Is(err, ErrOrigin) {
		t.Fatalf("a ceremony begun for another page: %v", err)
	}
}

// A replayed assertion does not approve again, and does not approve another
// approval; an approval edited in the store stops verifying at consume.
func TestReplayAndTampering(t *testing.T) {
	f := newFixture(t)
	key := newAuthenticator(t)
	if err := f.enroll(t, key, nil); err != nil {
		t.Fatal(err)
	}
	d, err := f.approve(t, key, pending())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Verify(pending(), d); !errors.Is(err, ErrCeremony) {
		t.Fatalf("the same assertion deciding twice: %v", err)
	}
	other := pending()
	other.ID = "apr_2"
	if err := f.svc.Verify(other, d); !errors.Is(err, ErrCeremony) {
		t.Fatalf("an assertion for another approval: %v", err)
	}
	edited := pending()
	edited.Status = approval.Approved
	edited.PlanHash = "sha256:swapped"
	if err := f.svc.Verify(edited, d); !errors.Is(err, ErrAssertion) {
		t.Fatalf("an approval edited in the store: %v", err)
	}
}

// A cloned authenticator shows as a counter that goes backwards.
func TestCounterGoingBackwardsIsRefused(t *testing.T) {
	f := newFixture(t)
	key := newAuthenticator(t)
	if err := f.enroll(t, key, nil); err != nil {
		t.Fatal(err)
	}
	key.Counter = 10
	if _, err := f.approve(t, key, pending()); err != nil {
		t.Fatal(err)
	}
	key.Counter = 3
	second := pending()
	second.ID = "apr_2"
	if _, err := f.approve(t, key, second); !errors.Is(err, ErrCloned) {
		t.Fatalf("a counter going backwards: %v", err)
	}
}

// Adding a second key, or removing one, needs an enrolled key's assertion.
func TestEnrollmentAfterTheFirstNeedsAnEnrolledKey(t *testing.T) {
	f := newFixture(t)
	first := newAuthenticator(t)
	if err := f.enroll(t, first, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.enroll(t, newAuthenticator(t), nil); !errors.Is(err, ErrNeedEnrolled) {
		t.Fatalf("a second key without an assertion: %v", err)
	}
	if err := f.enroll(t, newAuthenticator(t), newAuthenticator(t)); !errors.Is(err, ErrNeedEnrolled) {
		t.Fatalf("a second key authorized by a key that is not enrolled: %v", err)
	}
	second := newAuthenticator(t)
	if err := f.enroll(t, second, first); err != nil {
		t.Fatalf("a second key authorized by the first: %v", err)
	}
	if n := len(f.svc.Status().Keys); n != 2 {
		t.Fatalf("keys = %d", n)
	}
	if _, err := f.svc.BeginEnroll("not-a-token", origin, ""); !errors.Is(err, ErrEnrollToken) {
		t.Fatalf("a made-up token: %v", err)
	}

	fp := f.svc.Status().Keys[0].Fingerprint
	cer, opts, err := f.svc.BeginRemove(fp, origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.FinishRemove(human, cer, newAuthenticator(t).Assert(t, opts)); !errors.Is(err, ErrNeedEnrolled) {
		t.Fatalf("removal authorized by a key that is not enrolled: %v", err)
	}
	cer, opts, _ = f.svc.BeginRemove(fp, origin)
	if err := f.svc.FinishRemove(human, cer, second.Assert(t, opts)); err != nil {
		t.Fatalf("removal: %v", err)
	}
	if n := len(f.svc.Status().Keys); n != 1 {
		t.Fatalf("keys after removal = %d", n)
	}
}

// A registry changed outside Cerberus starts the cool-down: no out-of-band
// approval verifies, it is recorded with a notification, and it survives a
// restart; when it ends, the registry as it is becomes the recorded one.
func TestRegistryChangedOutsideStartsTheCooldown(t *testing.T) {
	f := newFixture(t)
	key := newAuthenticator(t)
	if err := f.enroll(t, key, nil); err != nil {
		t.Fatal(err)
	}
	d, err := f.approve(t, key, pending())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.dir, registryName)
	data, _ := os.ReadFile(path) //nolint:gosec // the test's own temp dir
	if err := os.WriteFile(path, append(data, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	approved := pending()
	approved.Status = approval.Approved
	var cooldown CooldownError
	if err := f.svc.Verify(approved, d); !errors.As(err, &cooldown) || cooldown.Until.Sub(f.now) != Cooldown {
		t.Fatalf("verify after an outside edit: %v", err)
	}
	if st := f.svc.Status(); st.State != StateCooldown {
		t.Fatalf("status = %+v", st)
	}
	if f.notified[len(f.notified)-1] != "Cerberus: passkey registry changed" {
		t.Fatalf("notifications = %v", f.notified)
	}

	// A restart reads the cool-down back from the audit log.
	restarted := New(f.dir, f.sink, Options{Records: f.sink.Records(), Origins: func() []string { return []string{origin} }, Now: func() time.Time { return f.now }})
	if err := restarted.Verify(approved, d); !errors.As(err, &cooldown) {
		t.Fatalf("verify after a restart: %v", err)
	}
	if _, err := restarted.BeginEnroll(allow(t, restarted), origin, ""); !errors.As(err, &cooldown) {
		t.Fatalf("enrolling during the cool-down: %v", err)
	}

	f.now = f.now.Add(Cooldown + time.Minute)
	if err := f.svc.Verify(approved, d); err != nil {
		t.Fatalf("verify after the cool-down: %v", err)
	}
	var changes []string
	for _, rec := range f.sink.Records() {
		if rec.Kind == audit.KindEnrollmentChanged {
			changes = append(changes, rec.Target.Fields["change"])
		}
	}
	if strings.Join(changes, ",") != "enrolled,unaudited,accepted_after_cooldown" {
		t.Fatalf("enrollment records = %v", changes)
	}
}
