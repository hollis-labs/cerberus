package cerbapi

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/userpresence"
)

// fakePresence answers the user-presence check without raising anything:
// no Touch ID prompt ever reaches a screen from a test.
type fakePresence struct {
	allow bool
	asked atomic.Int32
}

func (f *fakePresence) Verify(context.Context, string) error {
	f.asked.Add(1)
	if f.allow {
		return nil
	}
	return errors.Join(userpresence.ErrRefused, errors.New("the fake refused"))
}

// Tests run with a person present unless they say otherwise; enrollment is
// refused with no check installed at all.
func init() { SetUserPresence(&fakePresence{allow: true}) }

// withUserPresence installs a fake check for one test.
func withUserPresence(t *testing.T, allow bool) *fakePresence {
	t.Helper()
	f := &fakePresence{allow: allow}
	prev := userPresencePoint.Load()
	SetUserPresence(f)
	t.Cleanup(func() { userPresencePoint.Store(prev) })
	return f
}

// blockingPresence holds its prompt on screen until released.
type blockingPresence struct {
	asked   atomic.Int32
	shown   chan struct{}
	release chan error
}

func (b *blockingPresence) Verify(context.Context, string) error {
	b.asked.Add(1)
	select {
	case b.shown <- struct{}{}:
	case <-time.After(2 * time.Second):
		// Nobody was waiting for this prompt: a second one was raised.
		return errors.New("a second prompt was raised while one was on screen")
	}
	return <-b.release
}

// A caller cannot wear the person down with prompts: after one is refused,
// none is raised for PromptCooldown and the refusal says until when; one
// on screen blocks a second; an unavailable check starts no cool-down; and
// each prompt raised is recorded, while one the gate withholds is not.
func TestEnrollmentPromptsAreRationed(t *testing.T) {
	sink := audit.NewMemory()
	refusing := &fakePresence{allow: false}
	SetUserPresenceWith(refusing, sink)
	t.Cleanup(func() { SetUserPresence(&fakePresence{allow: true}) })
	gate := userPresencePoint.Load().gate
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	gate.now = func() time.Time { return now }
	person := callerAs(Principal{Kind: PrincipalHuman, Via: ViaCLI}, SurfaceSocket)

	if err := enrollPresenceRefusal(person); err == nil || !strings.Contains(err.Error(), "no prompt is raised for 5m0s") {
		t.Fatalf("a refused prompt: %v", err)
	}
	now = now.Add(time.Minute)
	err := enrollPresenceRefusal(person)
	if err == nil || !strings.Contains(err.Error(), "none is raised until") || !strings.Contains(err.Error(), "4m0s from now") || refusing.asked.Load() != 1 {
		t.Fatalf("within the cool-down: %v, asked %d", err, refusing.asked.Load())
	}
	if got := redact.Text(err.Error()); got != err.Error() {
		t.Fatalf("redaction rewrote the refusal: %q", got)
	}
	var intents, outcomes int
	for _, r := range sink.Records() {
		if r.Operation == "presence_prompt" {
			if r.Kind == audit.KindIntent {
				intents++
			} else {
				outcomes++
			}
		}
	}
	if intents != 1 || outcomes != 1 {
		t.Fatalf("records: %d intents, %d outcomes", intents, outcomes)
	}
	now = now.Add(5 * time.Minute)
	if err := enrollPresenceRefusal(person); err == nil || refusing.asked.Load() != 2 {
		t.Fatalf("after the cool-down it asks again: %v, asked %d", err, refusing.asked.Load())
	}

	// One prompt on screen at a time.
	blocking := &blockingPresence{shown: make(chan struct{}), release: make(chan error)}
	SetUserPresenceWith(blocking, sink)
	done := make(chan error)
	go func() { done <- enrollPresenceRefusal(person) }()
	<-blocking.shown
	if err := enrollPresenceRefusal(person); err == nil || !strings.Contains(err.Error(), "already on this Mac's screen") || blocking.asked.Load() != 1 {
		t.Fatalf("a second prompt while one is on screen: %v, asked %d", err, blocking.asked.Load())
	}
	blocking.release <- nil
	if err := <-done; err != nil {
		t.Fatalf("the prompt on screen, allowed: %v", err)
	}

	// Not being able to ask starts no cool-down.
	unavailable := &fakeUnavailable{}
	SetUserPresenceWith(unavailable, sink)
	for i := 0; i < 2; i++ {
		if err := enrollPresenceRefusal(person); err == nil {
			t.Fatal("an unavailable check allowed")
		}
	}
	if unavailable.asked.Load() != 2 {
		t.Fatalf("an unavailable check started a cool-down: asked %d", unavailable.asked.Load())
	}
}

type fakeUnavailable struct{ asked atomic.Int32 }

func (f *fakeUnavailable) Verify(context.Context, string) error {
	f.asked.Add(1)
	return userpresence.Refuse{Why: "no login session"}.Verify(context.Background(), "")
}
