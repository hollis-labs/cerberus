package secrets

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// A locked Secret Service does not fail: go-keyring asks D-Bus to unlock it
// and waits for the answer to an unlock prompt that, on a headless host after
// a reboot, nobody can give. The call then never returns, and whatever made it
// (the daemon's first audit write, a managed plugin's credential read) hangs
// with it, ahead of the socket. Every call into the OS credential store goes
// through KeyringCall, which bounds the wait, runs at most one call at a time
// and then fails fast for a while, so a locked store is a logged, non-fatal
// condition instead of an outage.

// KeyringTimeout is how long one call into the credential store may take.
var KeyringTimeout = 3 * time.Second

// KeyringCooldown is how long calls fail at once after one timed out, so a
// locked store costs a stall once, not on every credential read.
var KeyringCooldown = 2 * time.Minute

// KeyringUnavailableError says the credential store did not answer in time.
type KeyringUnavailableError struct {
	Op     string
	Reason string
}

func (e *KeyringUnavailableError) Error() string {
	return fmt.Sprintf("the OS credential store did not answer (%s): %s. It is most likely locked and waiting for an unlock prompt nobody can answer; unlock it, or supply the credential through its environment variable", e.Op, e.Reason)
}

var keyringGate struct {
	mu           sync.Mutex
	pending      bool
	blockedUntil time.Time
	// stuck is set when a call timed out and cleared when it finally returns;
	// stuckOp and stuckSince say which and since when.
	stuck      bool
	stuckOp    string
	stuckSince time.Time
}

// KeyringHealth is whether the OS credential store is answering: degraded
// while a call that timed out is still stuck, or during the fail-fast cooldown
// after one. The daemon reports it in its status.
type KeyringHealth struct {
	Degraded bool
	// Op is the call that timed out, Since when.
	Op    string
	Since time.Time
}

// Health reports the credential store's state as of the last calls.
func Health() KeyringHealth {
	keyringGate.mu.Lock()
	defer keyringGate.mu.Unlock()
	if keyringGate.stuck || time.Now().Before(keyringGate.blockedUntil) {
		return KeyringHealth{Degraded: true, Op: keyringGate.stuckOp, Since: keyringGate.stuckSince}
	}
	return KeyringHealth{}
}

type keyringResult[T any] struct {
	v   T
	err error
}

// KeyringCall runs fn, a call into the OS credential store, and returns its
// result, or a *KeyringUnavailableError if it does not finish within
// KeyringTimeout, while an earlier call is still stuck, or during the cooldown
// after a timeout. op names the call for the message. fn must not be retried
// by the caller on that error: it is still running in the background.
func KeyringCall[T any](op string, fn func() (T, error)) (T, error) {
	var zero T
	keyringGate.mu.Lock()
	switch {
	case keyringGate.pending:
		keyringGate.mu.Unlock()
		return zero, &KeyringUnavailableError{Op: op, Reason: "an earlier call is still waiting"}
	case time.Now().Before(keyringGate.blockedUntil):
		keyringGate.mu.Unlock()
		return zero, &KeyringUnavailableError{Op: op, Reason: "the store timed out a moment ago; not asking again yet"}
	}
	keyringGate.pending = true
	keyringGate.mu.Unlock()

	done := make(chan keyringResult[T], 1)
	go func() {
		v, err := fn()
		keyringGate.mu.Lock()
		keyringGate.pending = false
		keyringGate.stuck = false // it came back: the store answers again
		keyringGate.mu.Unlock()
		done <- keyringResult[T]{v, err}
	}()

	timer := time.NewTimer(KeyringTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.v, r.err
	case <-timer.C:
		keyringGate.mu.Lock()
		keyringGate.blockedUntil = time.Now().Add(KeyringCooldown)
		keyringGate.stuck, keyringGate.stuckOp, keyringGate.stuckSince = true, op, time.Now()
		keyringGate.mu.Unlock()
		slog.Default().Warn("secrets.keyring.unavailable", "op", op, "timeout", KeyringTimeout.String(), "cooldown", KeyringCooldown.String(),
			"consequence", "credentials in the OS store are unreadable until it is unlocked; the daemon keeps running")
		return zero, &KeyringUnavailableError{Op: op, Reason: fmt.Sprintf("no answer within %s", KeyringTimeout)}
	}
}

// ResetKeyringGate clears the timeout state; for tests.
func ResetKeyringGate() {
	keyringGate.mu.Lock()
	keyringGate.blockedUntil = time.Time{}
	keyringGate.stuck = false
	keyringGate.mu.Unlock()
}
