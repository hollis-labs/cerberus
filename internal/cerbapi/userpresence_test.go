package cerbapi

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

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
