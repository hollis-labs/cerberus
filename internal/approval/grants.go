package approval

import (
	"errors"
	"maps"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
)

// Grants (P3-5): a session or window approval covers every call of its
// operation on its target by its requester until it expires or is revoked.
// It never widens a deny (the caller consults it only for an approve
// decision), and every use is re-checked and recorded.

// ErrOtherTarget is a grant used for a target it does not cover.
var ErrOtherTarget = errors.New("the grant is for another target")

// GrantCheck is a call that would use a grant.
type GrantCheck struct {
	Connector string
	Operation string
	Target    audit.Target
	// Principal is the caller: a window grant is its requester's kind and
	// channel (an agent's grant never covers a human's call, or the
	// reverse), and a session grant its session's too.
	Principal        audit.Principal
	OperationID      string
	ReauthorizedDeny bool
	// RequireOutOfBand is policy as it reads now asking for out of band: a
	// grant met on a terminal no longer covers the call.
	RequireOutOfBand bool
}

// covers reports whether a grant covers a call, and why not.
func (a Approval) covers(c GrantCheck) error {
	switch {
	case c.Connector != a.Connector || c.Operation != a.Operation:
		return ErrOtherOperation
	case !sameTarget(a.Target, c.Target):
		return ErrOtherTarget
	case a.Principal.Kind != c.Principal.Kind || a.Principal.Via != c.Principal.Via || !sameCaller(a.Principal, c.Principal):
		return ErrOtherPrincipal
	case a.Scope == ScopeSession && (a.Principal.Session == "" || a.Principal.Session != c.Principal.Session):
		return ErrOtherPrincipal
	case c.RequireOutOfBand && a.Channel != ChannelOutOfBand:
		return ErrWeakerChannel
	}
	return nil
}

// sameTarget is the same target: its kind, resource and fields.
func sameTarget(a, b audit.Target) bool {
	return a.Kind == b.Kind && a.Resource == b.Resource && maps.Equal(a.Fields, b.Fields)
}

// UseGrant spends one use of the active grant that covers the call, if
// there is one, write-ahead like Consume. It returns ErrNotFound when no
// grant covers it; any other error is a grant that covers it but may not
// be used now (policy denies, presence does not verify).
func (s *Store) UseGrant(check GrantCheck, verifier PresenceVerifier) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var found *Approval
	for _, a := range s.state {
		view := a.viewAt(now)
		if !a.IsGrant() || view.Status != Approved || a.covers(check) != nil {
			continue
		}
		// The newest grant first, so the record names the one most
		// recently given.
		if found == nil || a.CreatedAt.After(found.CreatedAt) {
			found = a
		}
	}
	if found == nil {
		return Approval{}, ErrNotFound
	}
	return s.use(found, found.viewAt(now), check, verifier)
}

// use records one use of grant a. Called with s.mu held.
func (s *Store) use(a *Approval, view Approval, check GrantCheck, verifier PresenceVerifier) (Approval, error) {
	switch {
	case view.Status == Expired:
		return view, ErrExpired
	case view.Status != Approved:
		return view, ErrNotApproved
	case check.ReauthorizedDeny:
		return view, ErrPolicyNowDenys
	}
	if err := a.covers(check); err != nil {
		return view, err
	}
	if a.Channel == ChannelOutOfBand {
		if verifier == nil {
			verifier = NoPresence{}
		}
		if a.Decision == nil {
			return view, ErrNoPresence
		}
		if err := verifier.Verify(*a, *a.Decision); err != nil {
			return view, err
		}
	}
	if err := s.append(Event{Type: EventUsed, ApprovalID: a.ID, OperationID: check.OperationID}); err != nil {
		return Approval{}, err
	}
	return *s.state[a.ID], nil
}

// ActiveGrants are the grants usable now, newest first.
func (s *Store) ActiveGrants() []Approval {
	var out []Approval
	for _, a := range s.List() {
		if a.IsGrant() && a.Status == Approved {
			out = append(out, a)
		}
	}
	return out
}

// Remaining is how long an active grant has left.
func (a Approval) Remaining(now time.Time) time.Duration {
	if a.ExpiresAt.IsZero() {
		return 0
	}
	return a.ExpiresAt.Sub(now)
}
